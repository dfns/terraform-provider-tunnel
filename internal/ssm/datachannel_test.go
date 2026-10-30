package ssm

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/xtaci/smux"
)

// Vectors from the plugin's message/messageparser_test.go
// (TestSerializeAndDeserializeClientMessage, TestPutUuid), with the bytes its
// SerializeClientMessage produced for them.
var (
	goldenMessage = message{
		Type:          msgInputStreamData,
		SchemaVersion: 1,
		CreatedDate:   1503434274948,
		Seq:           1,
		Flags:         2,
		ID:            uuid.MustParse("dd01e56b-ff48-483e-a508-b5f073f31b16"),
		Payload:       []byte("payload"),
	}
	goldenFrame = mustDecodeHex(
		"00000074" + // header length 116
			"696e7075745f73747265616d5f64617461202020202020202020202020202020" + // type, space padded
			"00000001" + // schema version
			"0000015e0baa7884" + // created date
			"0000000000000001" + // seq
			"0000000000000002" + // flags
			"a508b5f073f31b16" + "dd01e56bff48483e" + // message id, low half first
			"239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5" + // sha256(payload)
			"00000000" + // payload type
			"00000007" + // payload length
			"7061796c6f6164", // payload
	)
)

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMessageMarshalMatchesPlugin(t *testing.T) {
	if got := goldenMessage.marshal(); !bytes.Equal(got, goldenFrame) {
		t.Fatalf("marshal() =\n%x\nwant\n%x", got, goldenFrame)
	}
}

func TestUnmarshalMessageRoundTrips(t *testing.T) {
	got, err := unmarshalMessage(goldenFrame)
	if err != nil {
		t.Fatal(err)
	}
	want := goldenMessage
	if got.Type != want.Type || got.SchemaVersion != want.SchemaVersion ||
		got.CreatedDate != want.CreatedDate || got.Seq != want.Seq || got.Flags != want.Flags ||
		got.ID != want.ID || got.PayloadType != want.PayloadType || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("unmarshalMessage() = %+v, want %+v", got, want)
	}
}

func TestUnmarshalMessageSkipsPublicationDigest(t *testing.T) {
	frame := message{Type: msgStartPublication, SchemaVersion: 1, CreatedDate: 1, ID: uuid.New(), Payload: []byte("{}")}.marshal()
	frame[len(frame)-1] ^= 0xff
	if _, err := unmarshalMessage(frame); err != nil {
		t.Fatalf("unmarshalMessage() error = %v, want start_publication accepted like the plugin does", err)
	}
}

func TestUnmarshalMessageRejectsMalformedFrames(t *testing.T) {
	tampered := bytes.Clone(goldenFrame)
	tampered[len(tampered)-1] ^= 0xff
	badHeaderLength := bytes.Clone(goldenFrame)
	badHeaderLength[3] = 0xff

	for name, frame := range map[string][]byte{
		"truncated header":     goldenFrame[:headerLength],
		"header past the end":  badHeaderLength,
		"payload digest wrong": tampered,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalMessage(frame); err == nil {
				t.Fatal("unmarshalMessage() succeeded, want error")
			}
		})
	}
}

const (
	testSessionID    = "session-1"
	testTargetID     = "i-0abc123"
	testToken        = "token-secret"
	testAgentVersion = "3.3.0.0"
)

var portSessionAction = requestedClientAction{
	ActionType:       "SessionType",
	ActionParameters: json.RawMessage(`{"SessionType":"Port","Properties":{"type":"LocalPortForwarding","portNumber":"5432"}}`),
}

// fakeAgent plays the SSM agent's side of the data channel, one session per
// WebSocket, driven by the test through agentSession.
type fakeAgent struct {
	*httptest.Server
	sessions chan *agentSession
	stop     chan struct{}
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	agent := &fakeAgent{sessions: make(chan *agentSession, 1), stop: make(chan struct{})}
	upgrader := websocket.Upgrader{}
	agent.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		s := &agentSession{
			conn:   conn,
			inputs: make(chan message, 2*inboxSize),
			acks:   make(chan int64, 2*inboxSize),
			done:   make(chan struct{}),
		}
		s.lostAck.Store(-1)
		if _, frame, err := conn.ReadMessage(); err != nil || json.Unmarshal(frame, &s.open) != nil {
			return
		}
		agent.sessions <- s
		s.readLoop(agent.stop)
	}))
	agent.Listener = smallReadBufferListener{agent.Listener}
	agent.Start()
	t.Cleanup(func() {
		close(agent.stop)
		agent.CloseClientConnections()
		agent.Close()
	})
	return agent
}

// smallReadBufferListener keeps socket buffers well under the window, so an
// agent that stops reading stalls the client's writes before the window fills.
type smallReadBufferListener struct{ net.Listener }

func (l smallReadBufferListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(16 << 10)
	}
	return conn, err
}

func (a *fakeAgent) session() SessionParams {
	return SessionParams{
		SessionId:  testSessionID,
		TokenValue: testToken,
		StreamUrl:  "ws" + strings.TrimPrefix(a.URL, "http") + "/v1/data-channel/" + testSessionID,
	}
}

func (a *fakeAgent) accept(t *testing.T) *agentSession {
	t.Helper()
	return receive(t, a.sessions)
}

type agentSession struct {
	conn *websocket.Conn
	open openDataChannelInput

	// inputs holds every input_stream_data message from the client, acks the
	// sequence numbers the client acknowledged.
	inputs       chan message
	acks         chan int64
	withholdAcks atomic.Bool
	lostAck      atomic.Int64 // a sequence number never acknowledged, -1 for none
	stalled      atomic.Bool
	done         chan struct{}

	writeMu sync.Mutex
	nextSeq int64

	// Set once KMS encryption is agreed, before the stream is used.
	encrypt cipher.AEAD
	decrypt cipher.AEAD
}

func (s *agentSession) readLoop(stop <-chan struct{}) {
	defer close(s.done)
	for {
		if s.stalled.Load() {
			<-stop
			return
		}
		_, frame, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := unmarshalMessage(frame)
		if err != nil {
			return
		}
		switch msg.Type {
		case msgAcknowledge:
			var ack acknowledgeContent
			if json.Unmarshal(msg.Payload, &ack) != nil {
				return
			}
			s.acks <- ack.SequenceNumber
		case msgInputStreamData:
			if !s.withholdAcks.Load() && msg.Seq != s.lostAck.Load() {
				content, _ := json.Marshal(acknowledgeContent{MessageType: msg.Type, MessageID: msg.ID.String(), SequenceNumber: msg.Seq})
				s.write(message{Type: msgAcknowledge, CreatedDate: 1, ID: uuid.New(), Payload: content})
			}
			s.inputs <- msg
		}
	}
}

func (s *agentSession) write(msg message) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteMessage(websocket.BinaryMessage, msg.marshal())
}

// sendSeq sends output_stream_data with an explicit sequence number, for tests
// that reorder or repeat them.
func (s *agentSession) sendSeq(seq int64, pt payloadType, payload []byte) {
	s.write(message{Type: msgOutputStreamData, SchemaVersion: 1, CreatedDate: 1, Seq: seq, ID: uuid.New(), PayloadType: pt, Payload: payload})
}

func (s *agentSession) send(pt payloadType, payload []byte) {
	s.writeMu.Lock()
	seq := s.nextSeq
	s.nextSeq++
	s.writeMu.Unlock()
	s.sendSeq(seq, pt, payload)
}

func (s *agentSession) sendJSON(t *testing.T, pt payloadType, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s.send(pt, body)
}

// expect returns the client's next message of the given payload type.
func (s *agentSession) expect(t *testing.T, pt payloadType) message {
	t.Helper()
	for {
		if msg := receive(t, s.inputs); msg.PayloadType == pt {
			return msg
		}
	}
}

func (s *agentSession) requestHandshake(t *testing.T, actions ...requestedClientAction) handshakeResponse {
	t.Helper()
	s.sendJSON(t, payloadHandshakeRequest, handshakeRequest{AgentVersion: testAgentVersion, RequestedClientActions: actions})
	var resp handshakeResponse
	if err := json.Unmarshal(s.expect(t, payloadHandshakeResponse).Payload, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func (s *agentSession) completeHandshake(t *testing.T) {
	t.Helper()
	s.sendJSON(t, payloadHandshakeComplete, map[string]any{"HandshakeTimeToComplete": 1000000, "CustomerMessage": ""})
}

// stream is the agent's end of the byte stream, reading the client's Output
// payloads in order.
func (s *agentSession) stream() io.ReadWriteCloser { return &agentStream{s: s} }

type agentStream struct {
	s       *agentSession
	pending []byte
}

func (a *agentStream) Read(p []byte) (int, error) {
	for len(a.pending) == 0 {
		select {
		case msg := <-a.s.inputs:
			if msg.PayloadType != payloadOutput {
				continue
			}
			a.pending = msg.Payload
			if a.s.decrypt != nil {
				var err error
				if a.pending, err = a.s.decrypt.Open(nil, nil, msg.Payload, nil); err != nil {
					return 0, err
				}
			}
		case <-a.s.done:
			return 0, io.EOF
		}
	}
	n := copy(p, a.pending)
	a.pending = a.pending[n:]
	return n, nil
}

func (a *agentStream) Write(p []byte) (int, error) {
	payload := bytes.Clone(p)
	if a.s.encrypt != nil {
		payload = a.s.encrypt.Seal(nil, nil, p, nil)
	}
	a.s.send(payloadOutput, payload)
	return len(p), nil
}

func (a *agentStream) Close() error { return nil }

// serveEcho runs the agent's smux server, echoing every stream.
func (s *agentSession) serveEcho(t *testing.T) {
	t.Helper()
	cfg := smux.DefaultConfig()
	cfg.KeepAliveDisabled = true
	mux, err := smux.Server(s.stream(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close() })
	go func() {
		for {
			stream, err := mux.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer stream.Close()
				_, _ = io.Copy(stream, stream)
			}()
		}
	}()
}

type dialResult struct {
	dc           *dataChannel
	agentVersion string
	err          error
}

func startDial(t *testing.T, agent *fakeAgent, kmsClient *kms.Client) (<-chan dialResult, *agentSession) {
	t.Helper()
	results := make(chan dialResult, 1)
	go func() {
		dc, agentVersion, err := dial(context.Background(), agent.session(), testTargetID, kmsClient)
		results <- dialResult{dc, agentVersion, err}
	}()
	return results, agent.accept(t)
}

// connect dials the fake agent through a plain port forwarding handshake.
func connect(t *testing.T, agent *fakeAgent) (*dataChannel, *agentSession) {
	t.Helper()
	results, s := startDial(t, agent, nil)
	s.requestHandshake(t, portSessionAction)
	s.completeHandshake(t)
	result := receive(t, results)
	if result.err != nil {
		t.Fatalf("dial() error = %v", result.err)
	}
	t.Cleanup(func() { _ = result.dc.Close() })
	return result.dc, s
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting on channel")
		panic("unreachable")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func seqPayload(seq int64) []byte { return binary.BigEndian.AppendUint16(nil, uint16(seq)) }

func TestDialSendsTokenAndAnswersHandshake(t *testing.T) {
	agent := newFakeAgent(t)
	results, s := startDial(t, agent, nil)

	if s.open.TokenValue != testToken || s.open.ClientVersion != clientVersion || s.open.MessageSchemaVersion != "1.0" {
		t.Fatalf("OpenDataChannelInput = %+v", s.open)
	}
	unknown := requestedClientAction{ActionType: "Telepathy", ActionParameters: json.RawMessage(`{}`)}
	resp := s.requestHandshake(t, portSessionAction, unknown)
	s.completeHandshake(t)

	result := receive(t, results)
	if result.err != nil {
		t.Fatalf("dial() error = %v", result.err)
	}
	defer result.dc.Close()
	if result.agentVersion != testAgentVersion {
		t.Errorf("agent version = %q, want %q", result.agentVersion, testAgentVersion)
	}
	got, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	// What the plugin answers to the same request.
	want := `{"ClientVersion":"1.2.694.0","ProcessedClientActions":[` +
		`{"ActionType":"SessionType","ActionStatus":1,"ActionResult":null,"Error":""},` +
		`{"ActionType":"Telepathy","ActionStatus":0,"ActionResult":3,"Error":"Unsupported action Telepathy"}],` +
		`"Errors":["Unsupported action Telepathy"]}`
	if string(got) != want {
		t.Fatalf("handshake response =\n%s\nwant\n%s", got, want)
	}
}

func TestDialRejectsOtherSessionTypes(t *testing.T) {
	t.Run("handshake for another session type", func(t *testing.T) {
		agent := newFakeAgent(t)
		results, s := startDial(t, agent, nil)

		shell := requestedClientAction{ActionType: "SessionType", ActionParameters: json.RawMessage(`{"SessionType":"Standard_Stream"}`)}
		resp := s.requestHandshake(t, shell)

		if got := resp.ProcessedClientActions[0]; got.ActionStatus != actionFailed || got.Error == "" {
			t.Errorf("processed action = %+v, want failed with an error", got)
		}
		if result := receive(t, results); result.err == nil {
			t.Fatal("dial() succeeded, want error")
		}
	})

	// A live shell document sends its prompt as the first message.
	t.Run("data without a handshake", func(t *testing.T) {
		agent := newFakeAgent(t)
		results, s := startDial(t, agent, nil)

		s.send(payloadOutput, []byte("$ "))

		if result := receive(t, results); result.err == nil {
			t.Fatal("dial() succeeded, want error")
		}
	})
}

func TestDataChannelReordersAndWindowsInbound(t *testing.T) {
	agent := newFakeAgent(t)
	dc, s := connect(t, agent)
	const next = 2 // after the handshake request and complete

	for seq := int64(next + 1); seq < next+inboxSize; seq++ {
		s.sendSeq(seq, payloadOutput, seqPayload(seq))
	}
	s.sendSeq(next+1, payloadOutput, seqPayload(next+1))                 // repeat of a held message
	s.sendSeq(next+inboxSize, payloadOutput, seqPayload(next+inboxSize)) // beyond the window
	s.sendSeq(next-1, payloadOutput, []byte("stale"))                    // already consumed
	s.sendSeq(next, payloadOutput, seqPayload(next))                     // the gap, last

	// Acks go out in processing order, so once the gap is acked every earlier
	// message has been either acked or dropped.
	acked := map[int64]bool{}
	for seq := int64(-1); seq != next; {
		seq = receive(t, s.acks)
		acked[seq] = true
	}
	for seq := int64(next); seq < next+inboxSize; seq++ {
		if !acked[seq] {
			t.Fatalf("seq %d inside the window was not acknowledged", seq)
		}
	}
	if acked[next+inboxSize] {
		t.Fatal("seq beyond the window was acknowledged")
	}
	if !acked[next-1] {
		t.Fatal("already consumed seq was not acknowledged again")
	}

	got := make([]byte, 2*inboxSize)
	if _, err := io.ReadFull(dc, got); err != nil {
		t.Fatal(err)
	}
	for i := range int64(inboxSize) {
		if want := seqPayload(next + i); !bytes.Equal(got[2*i:2*i+2], want) {
			t.Fatalf("read position %d = %x, want %x", i, got[2*i:2*i+2], want)
		}
	}

	// The window moved, so the resent message now fits.
	s.sendSeq(next+inboxSize, payloadOutput, seqPayload(next+inboxSize))
	if seq := receive(t, s.acks); seq != next+inboxSize {
		t.Fatalf("acknowledged seq %d, want %d", seq, next+inboxSize)
	}
	last := make([]byte, 2)
	if _, err := io.ReadFull(dc, last); err != nil {
		t.Fatal(err)
	}
	if want := seqPayload(next + inboxSize); !bytes.Equal(last, want) {
		t.Fatalf("resent message read as %x, want %x", last, want)
	}
}

func TestDataChannelDropsFlagsFromTheStream(t *testing.T) {
	agent := newFakeAgent(t)
	dc, s := connect(t, agent)

	s.send(payloadOutput, []byte("a"))
	s.send(payloadFlag, binary.BigEndian.AppendUint32(nil, flagConnectToPortError))
	s.send(payloadOutput, []byte("b"))

	got := make([]byte, 2)
	if _, err := io.ReadFull(dc, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ab" {
		t.Fatalf("read %q, want %q", got, "ab")
	}
}

// Acks for the client's writes keep being processed while nothing reads what
// the agent sends, and each one frees its window slot.
func TestDataChannelWritesPastTheWindowWhileNotReading(t *testing.T) {
	agent := newFakeAgent(t)
	dc, s := connect(t, agent)

	for range windowSize / 2 {
		s.send(payloadOutput, []byte("x"))
	}
	written := make(chan error, 1)
	go func() {
		_, err := dc.Write(make([]byte, 2*windowSize*payloadSize))
		written <- err
	}()
	if err := receive(t, written); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	waitFor(t, "every write to be acknowledged", func() bool {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		return len(dc.unacked) == 0
	})

	got := make([]byte, windowSize/2)
	if _, err := io.ReadFull(dc, got); err != nil {
		t.Fatal(err)
	}
}

func TestDataChannelFailsWithoutAcknowledgements(t *testing.T) {
	defer func(d time.Duration) { ackTimeout = d }(ackTimeout)
	// Long enough for paced writes to fill the window first.
	ackTimeout = 3 * time.Second

	agent := newFakeAgent(t)
	dc, s := connect(t, agent)
	s.withholdAcks.Store(true)

	type result struct {
		n   int
		err error
	}
	written := make(chan result, 1)
	go func() {
		n, err := dc.Write(make([]byte, (windowSize+1)*payloadSize))
		written <- result{n, err}
	}()
	got := receive(t, written)
	if got.n != windowSize*payloadSize {
		t.Errorf("Write() wrote %d bytes, want it to stop at the window (%d)", got.n, windowSize*payloadSize)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "no acknowledgement") {
		t.Fatalf("Write() error = %v, want no acknowledgement", got.err)
	}
	if !errors.Is(context.Cause(dc.Context()), got.err) {
		t.Errorf("channel cause = %v, want %v", context.Cause(dc.Context()), got.err)
	}
}

const testKMSKeyID = "alias/session-manager"

var (
	// testDataKey's halves are told apart so a swapped key cannot pass.
	testDataKey         = []byte("agent-to-client-key-is-32-bytes!client-to-agent-key-is-32-bytes!")
	testCiphertextBlob  = []byte("kms-ciphertext-blob")
	kmsEncryptionAction = requestedClientAction{
		ActionType:       "KMSEncryption",
		ActionParameters: json.RawMessage(`{"KMSKeyId":"` + testKMSKeyID + `"}`),
	}
)

type generateDataKeyRequest struct {
	KeyId             string
	NumberOfBytes     int32
	EncryptionContext map[string]string
}

// newFakeKMS serves GenerateDataKey, answering with testDataKey unless
// failure is set.
func newFakeKMS(t *testing.T, failure string) (*kms.Client, <-chan generateDataKeyRequest) {
	t.Helper()
	requests := make(chan generateDataKeyRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target := r.Header.Get("X-Amz-Target"); target != "TrentService.GenerateDataKey" {
			t.Errorf("KMS called with %q", target)
		}
		var req generateDataKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests <- req
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if failure != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"__type": "NotFoundException", "message": failure})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"KeyId":          req.KeyId,
			"CiphertextBlob": testCiphertextBlob,
			"Plaintext":      testDataKey,
		})
	}))
	t.Cleanup(server.Close)
	client := kms.New(kms.Options{
		Region:       "eu-west-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDTEST", "test-secret", ""),
		BaseEndpoint: aws.String(server.URL),
		HTTPClient:   server.Client(),
	})
	return client, requests
}

// useDataKey takes the agent's side of testDataKey.
func (s *agentSession) useDataKey(t *testing.T) {
	t.Helper()
	var err error
	if s.encrypt, err = newCipher(testDataKey[:32]); err != nil {
		t.Fatal(err)
	}
	if s.decrypt, err = newCipher(testDataKey[32:]); err != nil {
		t.Fatal(err)
	}
}

func (s *agentSession) challenge(t *testing.T) {
	t.Helper()
	want := make([]byte, 64)
	_, _ = rand.Read(want)
	s.sendJSON(t, payloadEncChallengeRequest, map[string][]byte{"Challenge": s.encrypt.Seal(nil, nil, want, nil)})
	var resp struct{ Challenge []byte }
	if err := json.Unmarshal(s.expect(t, payloadEncChallengeResponse).Payload, &resp); err != nil {
		t.Fatal(err)
	}
	got, err := s.decrypt.Open(nil, nil, resp.Challenge, nil)
	if err != nil {
		t.Fatalf("decrypting the challenge response: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("challenge response does not match the challenge")
	}
}

func TestDataChannelKMSEncryption(t *testing.T) {
	for name, challengeBeforeComplete := range map[string]bool{
		"challenge before handshake complete": true,
		"challenge after handshake complete":  false,
	} {
		t.Run(name, func(t *testing.T) {
			kmsClient, requests := newFakeKMS(t, "")
			agent := newFakeAgent(t)
			results, s := startDial(t, agent, kmsClient)

			resp := s.requestHandshake(t, portSessionAction, kmsEncryptionAction)
			req := receive(t, requests)
			if req.KeyId != testKMSKeyID || req.NumberOfBytes != 64 ||
				req.EncryptionContext["aws:ssm:SessionId"] != testSessionID ||
				req.EncryptionContext["aws:ssm:TargetId"] != testTargetID || len(req.EncryptionContext) != 2 {
				t.Errorf("GenerateDataKey request = %+v", req)
			}
			got := resp.ProcessedClientActions[1]
			result, _ := got.ActionResult.(map[string]any)
			if got.ActionStatus != actionSuccess || result["KMSCipherTextKey"] != base64.StdEncoding.EncodeToString(testCiphertextBlob) {
				t.Fatalf("KMSEncryption action = %+v, want success carrying the ciphertext blob", got)
			}

			s.useDataKey(t)
			if challengeBeforeComplete {
				s.challenge(t)
			}
			s.completeHandshake(t)
			dialed := receive(t, results)
			if dialed.err != nil {
				t.Fatalf("dial() error = %v", dialed.err)
			}
			defer dialed.dc.Close()

			cfg := smux.DefaultConfig()
			cfg.KeepAliveDisabled = true
			mux, err := smux.Client(dialed.dc, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer mux.Close()
			if !challengeBeforeComplete {
				// Answered from Read, which smux is now driving.
				s.challenge(t)
			}

			s.serveEcho(t)
			stream, err := mux.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			payload := bytes.Repeat([]byte("secret"), 1000)
			go func() { _, _ = stream.Write(payload) }()
			echoed := make([]byte, len(payload))
			if _, err := io.ReadFull(stream, echoed); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(echoed, payload) {
				t.Fatal("encrypted echo returned different bytes")
			}
		})
	}
}

func TestDialFailsWhenKMSFails(t *testing.T) {
	kmsClient, _ := newFakeKMS(t, "key not found")
	agent := newFakeAgent(t)
	results, s := startDial(t, agent, kmsClient)

	resp := s.requestHandshake(t, portSessionAction, kmsEncryptionAction)
	if got := resp.ProcessedClientActions[1]; got.ActionStatus != actionFailed || !strings.Contains(got.Error, "key not found") {
		t.Errorf("KMSEncryption action = %+v, want failed with the KMS error", got)
	}
	if result := receive(t, results); result.err == nil || !strings.Contains(result.err.Error(), "key not found") {
		t.Fatalf("dial() error = %v, want the KMS error", result.err)
	}
}

// Neither later acknowledgements nor a blocked write may hide a message that
// never gets acknowledged.
func TestDataChannelFailsWhenAcknowledgementsStall(t *testing.T) {
	defer func(d time.Duration) { ackTimeout = d }(ackTimeout)
	// Above resendAfter, so the resend a stalled write would pin does run.
	ackTimeout = resendAfter + time.Second

	writeUntilFailure := func(dc *dataChannel) <-chan error {
		failed := make(chan error, 1)
		go func() {
			for {
				if _, err := dc.Write(make([]byte, payloadSize)); err != nil {
					failed <- err
					return
				}
			}
		}()
		return failed
	}

	t.Run("one acknowledgement lost", func(t *testing.T) {
		agent := newFakeAgent(t)
		dc, s := connect(t, agent)
		// The handshake response took seq 0, so 1 is the first write.
		s.lostAck.Store(1)
		// Keeps the agent reading, so every later write is acknowledged.
		go func() {
			for {
				select {
				case <-s.inputs:
				case <-s.done:
					return
				}
			}
		}()

		start := time.Now()
		err := receive(t, writeUntilFailure(dc))
		if err == nil || !strings.Contains(err.Error(), "no acknowledgement") {
			t.Fatalf("Write() error = %v, want no acknowledgement", err)
		}
		if elapsed := time.Since(start); elapsed < ackTimeout {
			t.Fatalf("failed after %s, before the lost message was %s old", elapsed, ackTimeout)
		}
	})

	t.Run("agent stops reading", func(t *testing.T) {
		agent := newFakeAgent(t)
		dc, s := connect(t, agent)
		s.stalled.Store(true)

		if err := receive(t, writeUntilFailure(dc)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Write() error = %v, want the write deadline", err)
		}
	})
}

// tunnel is StartRemoteTunnel running against the fake agent, ready.
type tunnel struct {
	addr    string
	session *agentSession
	cancel  context.CancelFunc
	wait    func() error
}

func startTunnel(t *testing.T, agent *fakeAgent) *tunnel {
	t.Helper()
	port, err := libs.GetFreePort()
	if err != nil {
		t.Fatal(err)
	}
	session := agent.session()
	cfgJSON, err := json.Marshal(TunnelConfig{
		LocalPort:     strconv.Itoa(port),
		SSMInstance:   testTargetID,
		SSMRegion:     "eu-west-1",
		SessionParams: &session,
		Credentials:   &aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "test-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv(libs.TunnelReadyEnv, readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- StartRemoteTunnel(ctx, string(cfgJSON), os.Getpid()) }()
	wait := sync.OnceValue(func() error { return receive(t, done) })
	t.Cleanup(func() {
		cancel()
		_ = wait()
	})

	s := agent.accept(t)
	s.requestHandshake(t, portSessionAction)
	s.completeHandshake(t)
	waitFor(t, "the tunnel to be ready", func() bool {
		_, err := os.Stat(readyPath)
		return err == nil
	})
	return &tunnel{addr: net.JoinHostPort("localhost", strconv.Itoa(port)), session: s, cancel: cancel, wait: wait}
}

func (tun *tunnel) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", tun.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// stopWithin cancels the tunnel and fails unless StartRemoteTunnel returns nil
// within limit.
func (tun *tunnel) stopWithin(t *testing.T, limit time.Duration) {
	t.Helper()
	start := time.Now()
	tun.cancel()
	if err := tun.wait(); err != nil {
		t.Fatalf("StartRemoteTunnel() = %v, want nil after cancel", err)
	}
	if elapsed := time.Since(start); elapsed > limit {
		t.Fatalf("StartRemoteTunnel() took %s to stop, want under %s", elapsed, limit)
	}
}

func TestStartRemoteTunnelForwardsConcurrentConnections(t *testing.T) {
	agent := newFakeAgent(t)
	tun := startTunnel(t, agent)
	tun.session.serveEcho(t)

	var wg sync.WaitGroup
	for i := range 2 {
		conn := tun.dial(t)
		wg.Go(func() {
			// Several payloads' worth, so chunking and reassembly both run.
			payload := bytes.Repeat([]byte{byte('a' + i)}, 50*payloadSize+7)
			go func() { _, _ = conn.Write(payload) }()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("connection %d echoed different bytes", i)
			}
		})
	}
	wg.Wait()
}

func TestStartRemoteTunnelStopsWithinBudget(t *testing.T) {
	// Leaves room above terminateTimeout, which a stalled write spends in full.
	const limit = terminateTimeout + time.Second

	t.Run("relay blocked on a full window", func(t *testing.T) {
		agent := newFakeAgent(t)
		tun := startTunnel(t, agent)
		tun.session.withholdAcks.Store(true)
		conn := tun.dial(t)
		go func() { _, _ = conn.Write(make([]byte, 2*windowSize*payloadSize)) }()
		waitFor(t, "the window to fill", func() bool { return len(tun.session.inputs) >= windowSize })

		tun.stopWithin(t, limit)
	})

	t.Run("WebSocket write stalled", func(t *testing.T) {
		agent := newFakeAgent(t)
		tun := startTunnel(t, agent)
		tun.session.stalled.Store(true)
		conn := tun.dial(t)
		go func() { _, _ = conn.Write(make([]byte, 8*windowSize*payloadSize)) }()
		// Long enough for the agent to stop reading and the socket buffers to fill.
		time.Sleep(300 * time.Millisecond)

		tun.stopWithin(t, limit)
	})
}

func TestStartRemoteTunnelTerminatesSessionOnStop(t *testing.T) {
	agent := newFakeAgent(t)
	tun := startTunnel(t, agent)

	tun.stopWithin(t, terminateTimeout)

	flag := tun.session.expect(t, payloadFlag)
	if want := binary.BigEndian.AppendUint32(nil, flagTerminateSession); !bytes.Equal(flag.Payload, want) {
		t.Fatalf("flag payload = %x, want TerminateSession %x", flag.Payload, want)
	}
	receive(t, tun.session.done)
}

func TestStartRemoteTunnelEndsWhenAgentClosesChannel(t *testing.T) {
	agent := newFakeAgent(t)
	tun := startTunnel(t, agent)

	closed, err := json.Marshal(map[string]any{"MessageType": msgChannelClosed, "SessionId": testSessionID, "Output": "terminated by an administrator"})
	if err != nil {
		t.Fatal(err)
	}
	tun.session.write(message{Type: msgChannelClosed, SchemaVersion: 1, CreatedDate: 1, ID: uuid.New(), Payload: closed})

	if err := tun.wait(); err == nil || !strings.Contains(err.Error(), "terminated by an administrator") {
		t.Fatalf("StartRemoteTunnel() = %v, want the agent's reason", err)
	}
}

func TestMuxConfigAppliesAgentVersionGates(t *testing.T) {
	for _, tc := range []struct {
		version          string
		supported        bool
		keepAliveEnabled bool
	}{
		{version: "3.0.195.0"},
		{version: "3.0.196.0"},
		{version: "3.0.196.1", supported: true, keepAliveEnabled: true},
		{version: "3.1.1511.0", supported: true, keepAliveEnabled: true},
		{version: "3.1.1511.1", supported: true},
		{version: "3.3.0.0", supported: true},
		{version: "3.3"},
		{version: ""},
	} {
		t.Run(tc.version, func(t *testing.T) {
			cfg, err := muxConfig(tc.version)
			if !tc.supported {
				if err == nil {
					t.Fatal("muxConfig() succeeded, want too old")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.KeepAliveDisabled == tc.keepAliveEnabled {
				t.Errorf("KeepAliveDisabled = %t, want %t", cfg.KeepAliveDisabled, !tc.keepAliveEnabled)
			}
		})
	}
}
