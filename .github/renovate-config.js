/**
 * @type {import('renovate/dist/config/types').AllConfig}
 */
module.exports = {
  autodiscover: false,
  branchPrefix: "renovate/",
  dryRun: process.env.RENOVATE_REPOSITORIES ? null : "full",
  enabledManagers: ["gomod", "github-actions"],
  gitAuthor: "dfns-github-bot <infra@dfns.co>",
  onboarding: false,
  platform: "github",
  postUpdateOptions: ["gomodTidy"],
  prConcurrentLimit: 0,
  prHourlyLimit: 0,
  requireConfig: "optional",
  lockFileMaintenance: {
    enabled: false,
    schedule: null,
  },
  packageRules: [
    {
      groupName: "aws-sdk-go-v2 packages",
      groupSlug: "aws-sdk-go-v2",
      matchDatasources: ["go"],
      matchPackageNames: ["github.com/aws/aws-sdk-go-v2/**"],
    },
  ],
  customManagers: [],
};
