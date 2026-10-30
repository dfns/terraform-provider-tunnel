package ssm

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// The SSM data channel protocol, as implemented by the Session Manager plugin
// fork this replaced, last checked at
// https://github.com/formalco/session-manager-plugin/tree/ea0a42ddcaf7/src

const (
	// The plugin version the fork sent, so agents see the client they always have.
	clientVersion = "1.2.694.0"
	payloadSize   = 1024
	// windowSize bounds the unacked outgoing messages.
	windowSize = 1000
	// inboxSize bounds how far ahead of the consumer inbound messages are held.
	// It is the plugin's size: the agent recovers drops one resend per tick, so
	// a local reader stalled past a smaller inbox leaves the tunnel crawling.
	inboxSize        = 10000
	resendTick       = 100 * time.Millisecond
	resendAfter      = time.Second
	pingInterval     = 5 * time.Minute
	terminateTimeout = time.Second
)

// Variables so tests can change them.
var (
	// ackTimeout bounds how long a message may stay unacknowledged, and a write
	// may block.
	ackTimeout = 5 * time.Minute
	// MGS drops bursts above about 1.3k messages per second, and each loss then
	// costs a resend tick, so Write paces itself like the plugin does.
	paceInterval = time.Millisecond
)

const (
	msgInputStreamData  = "input_stream_data"
	msgOutputStreamData = "output_stream_data"
	msgAcknowledge      = "acknowledge"
	msgChannelClosed    = "channel_closed"
	msgStartPublication = "start_publication"
	msgPausePublication = "pause_publication"
)

type payloadType uint32

const (
	payloadOutput               payloadType = 1
	payloadHandshakeRequest     payloadType = 5
	payloadHandshakeResponse    payloadType = 6
	payloadHandshakeComplete    payloadType = 7
	payloadEncChallengeRequest  payloadType = 8
	payloadEncChallengeResponse payloadType = 9
	payloadFlag                 payloadType = 10
)

// Header field sizes, in wire order. The header length field counts every
// header byte except the payload length.
const (
	messageTypeSize = 32
	headerLength    = 4 + messageTypeSize + 4 + 8 + 8 + 8 + 16 + sha256.Size + 4
)

type message struct {
	Type          string
	SchemaVersion uint32
	CreatedDate   uint64
	Seq           int64
	Flags         uint64
	ID            uuid.UUID
	PayloadType   payloadType
	Payload       []byte
}

func (m message) marshal() []byte {
	b := make([]byte, 0, headerLength+4+len(m.Payload))
	b = binary.BigEndian.AppendUint32(b, headerLength)
	b = append(b, bytes.Repeat([]byte{' '}, messageTypeSize)...)
	copy(b[4:], m.Type)
	b = binary.BigEndian.AppendUint32(b, m.SchemaVersion)
	b = binary.BigEndian.AppendUint64(b, m.CreatedDate)
	b = binary.BigEndian.AppendUint64(b, uint64(m.Seq))
	b = binary.BigEndian.AppendUint64(b, m.Flags)
	// The UUID goes on the wire as its low half, then its high half.
	b = append(b, m.ID[8:]...)
	b = append(b, m.ID[:8]...)
	digest := sha256.Sum256(m.Payload)
	b = append(b, digest[:]...)
	b = binary.BigEndian.AppendUint32(b, uint32(m.PayloadType))
	b = binary.BigEndian.AppendUint32(b, uint32(len(m.Payload)))
	return append(b, m.Payload...)
}

func unmarshalMessage(b []byte) (message, error) {
	if len(b) < headerLength+4 {
		return message{}, errors.New("SSM message shorter than its header")
	}
	hl := binary.BigEndian.Uint32(b)
	if hl < headerLength || uint64(hl)+4 > uint64(len(b)) {
		return message{}, errors.New("invalid SSM message header length")
	}
	var m message
	m.Type = string(bytes.TrimSpace(bytes.Trim(b[4:4+messageTypeSize], "\x00")))
	r := b[4+messageTypeSize:]
	m.SchemaVersion = binary.BigEndian.Uint32(r)
	m.CreatedDate = binary.BigEndian.Uint64(r[4:])
	m.Seq = int64(binary.BigEndian.Uint64(r[12:]))
	m.Flags = binary.BigEndian.Uint64(r[20:])
	copy(m.ID[8:], r[28:36])
	copy(m.ID[:8], r[36:44])
	digest := r[44 : 44+sha256.Size]
	m.PayloadType = payloadType(binary.BigEndian.Uint32(r[44+sha256.Size:]))
	m.Payload = b[hl+4:]
	// MGS sends these with a digest that does not match, and the plugin skips
	// checking them too.
	if m.Type == msgStartPublication || m.Type == msgPausePublication {
		return m, nil
	}
	if sum := sha256.Sum256(m.Payload); len(m.Payload) > 0 && !bytes.Equal(sum[:], digest) {
		return message{}, errors.New("invalid SSM message payload digest")
	}
	return m, nil
}

const (
	flagTerminateSession   = 2
	flagConnectToPortError = 3
)

type openDataChannelInput struct {
	MessageSchemaVersion string
	RequestId            string
	TokenValue           string
	ClientId             string
	ClientVersion        string
}

type acknowledgeContent struct {
	MessageType         string `json:"AcknowledgedMessageType"`
	MessageID           string `json:"AcknowledgedMessageId"`
	SequenceNumber      int64  `json:"AcknowledgedMessageSequenceNumber"`
	IsSequentialMessage bool
}

type handshakeRequest struct {
	AgentVersion           string
	RequestedClientActions []requestedClientAction
}

type requestedClientAction struct {
	ActionType       string
	ActionParameters json.RawMessage
}

type handshakeResponse struct {
	ClientVersion          string
	ProcessedClientActions []processedClientAction
	Errors                 []string
}

type processedClientAction struct {
	ActionType   string
	ActionStatus actionStatus
	ActionResult any
	Error        string
}

type actionStatus int

const (
	actionSuccess     actionStatus = 1
	actionFailed      actionStatus = 2
	actionUnsupported actionStatus = 3
)

type sentMessage struct {
	seq       int64
	frame     []byte
	firstSent time.Time
	sentAt    time.Time
}

// dataChannel carries one SSM session as a byte stream. It ends once, when its
// context does: the cause is the parent's when the tunnel is stopped,
// net.ErrClosed after Close, and the channel failure otherwise.
type dataChannel struct {
	ws         *websocket.Conn
	ackTimeout time.Duration
	parent     context.Context
	ctx        context.Context
	cancel     context.CancelCauseFunc
	closed     chan struct{}

	// gorilla allows one concurrent writer.
	writeMu sync.Mutex

	mu sync.Mutex
	// changed is closed and replaced whenever the inbox or the window changes.
	changed chan struct{}
	nextSeq int64
	unacked []*sentMessage
	inbox   map[int64]message
	next    int64

	// Only touched by the consumer, dial then Read. Write uses encrypt too, but
	// only once dial has returned.
	pending []byte
	encrypt cipher.AEAD
	decrypt cipher.AEAD
}

// dial opens the session's data channel and returns once the agent completed
// the handshake, with the agent's version. kmsClient is only called when the
// session document asks for KMS encryption.
func dial(ctx context.Context, session SessionParams, targetID string, kmsClient *kms.Client) (*dataChannel, string, error) {
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, session.StreamUrl, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, "", fmt.Errorf("connect SSM data channel: %w", err)
	}

	dcCtx, cancel := context.WithCancelCause(ctx)
	dc := &dataChannel{
		ws:         ws,
		ackTimeout: ackTimeout,
		parent:     ctx,
		ctx:        dcCtx,
		cancel:     cancel,
		closed:     make(chan struct{}),
		changed:    make(chan struct{}),
		inbox:      make(map[int64]message),
	}
	context.AfterFunc(dcCtx, dc.shutdown)

	encryptionContext := map[string]string{"aws:ssm:SessionId": session.SessionId, "aws:ssm:TargetId": targetID}
	agentVersion, err := dc.open(session.TokenValue, kmsClient, encryptionContext)
	if err != nil {
		dc.cancel(err)
		<-dc.closed
		return nil, "", err
	}
	return dc, agentVersion, nil
}

func (dc *dataChannel) open(token string, kmsClient *kms.Client, encryptionContext map[string]string) (string, error) {
	input, err := json.Marshal(openDataChannelInput{
		MessageSchemaVersion: "1.0",
		RequestId:            uuid.NewString(),
		TokenValue:           token,
		ClientId:             uuid.NewString(),
		ClientVersion:        clientVersion,
	})
	if err != nil {
		return "", err
	}
	if err := dc.write(websocket.TextMessage, input); err != nil {
		return "", err
	}
	go dc.readLoop()
	go dc.maintain()

	var agentVersion string
	for {
		msg, err := dc.nextMessage()
		if err != nil {
			return "", err
		}
		switch msg.PayloadType {
		case payloadHandshakeRequest:
			if agentVersion, err = dc.answerHandshake(msg.Payload, kmsClient, encryptionContext); err != nil {
				return "", err
			}
		case payloadHandshakeComplete:
			return agentVersion, nil
		case payloadOutput:
			// What a shell document does: it starts streaming without a handshake.
			return "", errors.New("SSM agent sent data before the handshake, the document is not a port forwarding session")
		default:
			if _, err := dc.consume(msg); err != nil {
				return "", err
			}
		}
	}
}

func (dc *dataChannel) answerHandshake(payload []byte, kmsClient *kms.Client, encryptionContext map[string]string) (string, error) {
	var req handshakeRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return "", fmt.Errorf("decode SSM handshake request: %w", err)
	}

	resp := handshakeResponse{ClientVersion: clientVersion, ProcessedClientActions: []processedClientAction{}}
	var failed []error
	for _, action := range req.RequestedClientActions {
		processed := processedClientAction{ActionType: action.ActionType}
		var err error
		switch action.ActionType {
		case "SessionType":
			err = checkSessionType(action.ActionParameters)
		case "KMSEncryption":
			processed.ActionResult, err = dc.setUpEncryption(kmsClient, encryptionContext, action.ActionParameters)
		default:
			// The plugin reports the status in ActionResult here, not ActionStatus.
			processed.ActionResult = actionUnsupported
			processed.Error = "Unsupported action " + action.ActionType
			resp.Errors = append(resp.Errors, processed.Error)
			resp.ProcessedClientActions = append(resp.ProcessedClientActions, processed)
			continue
		}
		if err != nil {
			processed.ActionStatus = actionFailed
			processed.Error = fmt.Sprintf("Failed to process action %s: %s", action.ActionType, err)
			resp.Errors = append(resp.Errors, err.Error())
			failed = append(failed, err)
		} else {
			processed.ActionStatus = actionSuccess
		}
		resp.ProcessedClientActions = append(resp.ProcessedClientActions, processed)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	if err := dc.send(payloadHandshakeResponse, body); err != nil {
		return "", err
	}
	return req.AgentVersion, errors.Join(failed...)
}

// checkSessionType accepts the one session the plugin multiplexes TCP over.
func checkSessionType(params json.RawMessage) error {
	var req struct {
		SessionType string
		Properties  struct{ Type string }
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return err
	}
	if req.SessionType != "Port" || req.Properties.Type != "LocalPortForwarding" {
		return fmt.Errorf("unsupported session type %q (%q), need Port (LocalPortForwarding)",
			req.SessionType, req.Properties.Type)
	}
	return nil
}

// setUpEncryption has KMS generate the session keys. The agent decrypts the
// returned ciphertext to get the same ones.
func (dc *dataChannel) setUpEncryption(kmsClient *kms.Client, encryptionContext map[string]string, params json.RawMessage) (any, error) {
	var req struct{ KMSKeyId string }
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	out, err := kmsClient.GenerateDataKey(dc.ctx, &kms.GenerateDataKeyInput{
		KeyId:             aws.String(req.KMSKeyId),
		NumberOfBytes:     aws.Int32(64),
		EncryptionContext: encryptionContext,
	})
	if err != nil {
		return nil, fmt.Errorf("generate KMS data key: %w", err)
	}
	if len(out.Plaintext) != 64 {
		return nil, fmt.Errorf("KMS returned a %d-byte data key, want 64", len(out.Plaintext))
	}
	// The first half encrypts what the agent sends, the second what it receives.
	if dc.decrypt, err = newCipher(out.Plaintext[:32]); err != nil {
		return nil, err
	}
	if dc.encrypt, err = newCipher(out.Plaintext[32:]); err != nil {
		return nil, err
	}
	return struct{ KMSCipherTextKey []byte }{out.CiphertextBlob}, nil
}

// newCipher matches the plugin's: AES-256-GCM, with the random 12-byte nonce
// prepended to the ciphertext.
func newCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (dc *dataChannel) answerChallenge(payload []byte) error {
	if dc.decrypt == nil {
		return errors.New("SSM encryption challenge without KMS encryption")
	}
	var req struct{ Challenge []byte }
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("decode SSM encryption challenge: %w", err)
	}
	challenge, err := dc.decrypt.Open(nil, nil, req.Challenge, nil)
	if err != nil {
		return fmt.Errorf("decrypt SSM encryption challenge: %w", err)
	}
	body, err := json.Marshal(struct{ Challenge []byte }{dc.encrypt.Seal(nil, nil, challenge, nil)})
	if err != nil {
		return err
	}
	return dc.send(payloadEncChallengeResponse, body)
}

// consume handles a message outside the handshake and returns its stream data.
func (dc *dataChannel) consume(msg message) ([]byte, error) {
	switch msg.PayloadType {
	case payloadOutput:
		if dc.decrypt == nil {
			return msg.Payload, nil
		}
		data, err := dc.decrypt.Open(nil, nil, msg.Payload, nil)
		if err != nil {
			return nil, fmt.Errorf("decrypt SSM payload: %w", err)
		}
		return data, nil
	case payloadEncChallengeRequest:
		return nil, dc.answerChallenge(msg.Payload)
	case payloadFlag:
		if bytes.Equal(msg.Payload, binary.BigEndian.AppendUint32(nil, flagConnectToPortError)) {
			log.Print("SSM agent failed to connect to the target port, check its logs")
		}
	}
	return nil, nil
}

func (dc *dataChannel) Context() context.Context { return dc.ctx }

func (dc *dataChannel) Read(p []byte) (int, error) {
	for len(dc.pending) == 0 {
		msg, err := dc.nextMessage()
		if err != nil {
			return 0, err
		}
		if dc.pending, err = dc.consume(msg); err != nil {
			dc.cancel(err)
			return 0, err
		}
	}
	n := copy(p, dc.pending)
	dc.pending = dc.pending[n:]
	return n, nil
}

func (dc *dataChannel) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if err := dc.waitWindow(); err != nil {
			return written, err
		}
		chunk := p[:min(len(p), payloadSize)]
		payload := chunk
		if dc.encrypt != nil {
			payload = dc.encrypt.Seal(nil, nil, chunk, nil)
		}
		if err := dc.send(payloadOutput, payload); err != nil {
			return written, err
		}
		time.Sleep(paceInterval)
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// Close sends the terminate flag and waits until the WebSocket is closed.
func (dc *dataChannel) Close() error {
	dc.cancel(net.ErrClosed)
	<-dc.closed
	return nil
}

func (dc *dataChannel) shutdown() {
	defer close(dc.closed)
	// A failed channel has nothing left to tell the agent.
	if dc.parent.Err() != nil || errors.Is(context.Cause(dc.ctx), net.ErrClosed) {
		sent := make(chan struct{})
		go func() {
			defer close(sent)
			_ = dc.send(payloadFlag, binary.BigEndian.AppendUint32(nil, flagTerminateSession))
		}()
		select {
		case <-sent:
		case <-time.After(terminateTimeout):
		}
	}
	// Unblocks a write stalled while holding writeMu.
	_ = dc.ws.Close()
}

// readLoop never waits on the consumer, so acknowledgements keep flowing while
// the local side is not reading.
func (dc *dataChannel) readLoop() {
	for {
		_, frame, err := dc.ws.ReadMessage()
		if err != nil {
			dc.cancel(fmt.Errorf("read SSM data channel: %w", err))
			return
		}
		msg, err := unmarshalMessage(frame)
		if err != nil {
			log.Printf("dropping SSM message: %v", err)
			continue
		}
		switch msg.Type {
		case msgOutputStreamData:
			dc.receive(msg)
		case msgAcknowledge:
			dc.acknowledge(msg)
		case msgChannelClosed:
			var closed struct{ Output string }
			_ = json.Unmarshal(msg.Payload, &closed)
			dc.cancel(fmt.Errorf("SSM agent closed the channel: %s", closed.Output))
			return
		}
	}
}

func (dc *dataChannel) receive(msg message) {
	dc.mu.Lock()
	// Past the window it is left unacknowledged, so the agent resends it.
	if msg.Seq >= dc.next+inboxSize {
		dc.mu.Unlock()
		return
	}
	// One already consumed is acknowledged again, in case the first ack was lost.
	if msg.Seq >= dc.next {
		dc.inbox[msg.Seq] = msg
		dc.notifyLocked()
	}
	dc.mu.Unlock()

	content, err := json.Marshal(acknowledgeContent{
		MessageType:         msg.Type,
		MessageID:           msg.ID.String(),
		SequenceNumber:      msg.Seq,
		IsSequentialMessage: true,
	})
	if err != nil {
		dc.cancel(err)
		return
	}
	_ = dc.write(websocket.BinaryMessage, message{
		Type:          msgAcknowledge,
		SchemaVersion: 1,
		CreatedDate:   uint64(time.Now().UnixMilli()),
		Flags:         3,
		ID:            uuid.New(),
		Payload:       content,
	}.marshal())
}

func (dc *dataChannel) acknowledge(msg message) {
	var ack acknowledgeContent
	if err := json.Unmarshal(msg.Payload, &ack); err != nil {
		log.Printf("dropping SSM acknowledgement: %v", err)
		return
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	i := slices.IndexFunc(dc.unacked, func(m *sentMessage) bool { return m.seq == ack.SequenceNumber })
	if i < 0 {
		return
	}
	dc.unacked = slices.Delete(dc.unacked, i, i+1)
	dc.notifyLocked()
}

func (dc *dataChannel) notifyLocked() {
	close(dc.changed)
	dc.changed = make(chan struct{})
}

func (dc *dataChannel) nextMessage() (message, error) {
	for {
		dc.mu.Lock()
		msg, ok := dc.inbox[dc.next]
		if ok {
			delete(dc.inbox, dc.next)
			dc.next++
		}
		changed := dc.changed
		dc.mu.Unlock()
		if ok {
			return msg, nil
		}
		select {
		case <-changed:
		case <-dc.ctx.Done():
			return message{}, context.Cause(dc.ctx)
		}
	}
}

func (dc *dataChannel) waitWindow() error {
	for {
		dc.mu.Lock()
		full := len(dc.unacked) >= windowSize
		changed := dc.changed
		dc.mu.Unlock()
		if !full {
			return nil
		}
		select {
		case <-changed:
		case <-dc.ctx.Done():
			return context.Cause(dc.ctx)
		}
	}
}

// send records the message before writing it, so an acknowledgement can never
// arrive for a message the window does not know yet.
func (dc *dataChannel) send(pt payloadType, payload []byte) error {
	dc.mu.Lock()
	now := time.Now()
	frame := message{
		Type:          msgInputStreamData,
		SchemaVersion: 1,
		CreatedDate:   uint64(now.UnixMilli()),
		Seq:           dc.nextSeq,
		ID:            uuid.New(),
		PayloadType:   pt,
		Payload:       payload,
	}.marshal()
	dc.unacked = append(dc.unacked, &sentMessage{seq: dc.nextSeq, frame: frame, firstSent: now, sentAt: now})
	dc.nextSeq++
	dc.mu.Unlock()
	return dc.write(websocket.BinaryMessage, frame)
}

func (dc *dataChannel) write(messageType int, data []byte) error {
	dc.writeMu.Lock()
	defer dc.writeMu.Unlock()
	// A peer that stops reading would otherwise hold every writer, the resend
	// loop that enforces ackTimeout included.
	_ = dc.ws.SetWriteDeadline(time.Now().Add(dc.ackTimeout))
	if err := dc.ws.WriteMessage(messageType, data); err != nil {
		dc.cancel(fmt.Errorf("write SSM data channel: %w", err))
		return context.Cause(dc.ctx)
	}
	return nil
}

// maintain resends the oldest unacknowledged message, as the plugin does, and
// keeps the WebSocket alive.
func (dc *dataChannel) maintain() {
	resend := time.NewTicker(resendTick)
	defer resend.Stop()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-dc.ctx.Done():
			return
		case <-ping.C:
			_ = dc.write(websocket.PingMessage, []byte("keepalive"))
		case <-resend.C:
			dc.resendOldest()
		}
	}
}

func (dc *dataChannel) resendOldest() {
	dc.mu.Lock()
	if len(dc.unacked) == 0 {
		dc.mu.Unlock()
		return
	}
	oldest := dc.unacked[0]
	if time.Since(oldest.firstSent) > dc.ackTimeout {
		dc.mu.Unlock()
		dc.cancel(errors.New("no acknowledgement from SSM agent"))
		return
	}
	if time.Since(oldest.sentAt) < resendAfter {
		dc.mu.Unlock()
		return
	}
	oldest.sentAt = time.Now()
	frame := oldest.frame
	dc.mu.Unlock()
	_ = dc.write(websocket.BinaryMessage, frame)
}
