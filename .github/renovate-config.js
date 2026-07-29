/**
 * @type {import('renovate/dist/config/types').AllConfig}
 */
module.exports = {
  autodiscover: false,
  branchPrefix: "renovate/",
  dryRun: process.env.RENOVATE_REPOSITORIES ? null : "full",
  enabledManagers: ["gomod", "github-actions"],
  onboarding: false,
  platform: "github",
  postUpdateOptions: ["gomodTidy"],
  prConcurrentLimit: 0,
  prHourlyLimit: 0,
  minimumReleaseAge: "7 days",
  internalChecksFilter: "strict",
  semanticCommits: "enabled",
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
      matchPackageNames: [
        "github.com/aws/aws-sdk-go-v2",
        "github.com/aws/aws-sdk-go-v2/**",
        "github.com/aws/smithy-go",
      ],
    },
    {
      groupName: "azure-sdk-for-go packages",
      groupSlug: "azure-sdk-for-go",
      matchDatasources: ["go"],
      matchPackageNames: [
        "github.com/Azure/azure-sdk-for-go/**",
        "github.com/AzureAD/microsoft-authentication-library-for-go",
      ],
    },
    {
      groupName: "kubernetes packages",
      groupSlug: "kubernetes",
      matchDatasources: ["go"],
      matchPackageNames: ["k8s.io/**", "sigs.k8s.io/**"],
    },
  ],
  customManagers: [],
};
