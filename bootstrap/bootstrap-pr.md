Title: Provision the Hugging Face provider GitHub repository

Add the public terraform-provider-huggingface repository, infrastructure-owned
MPL-2.0 LICENSE and existing GPG signing-secret convention. No HF provider
requirement is added, so repository bootstrap does not depend on a release from
the repository it creates.

Patch: bootstrap.patch. Isolated tofu fmt/init/validate and patch applicability
passed. A real plan/apply requires existing App/backend credentials and explicit
authorization; neither ran. Provider code is a separate local branch/PR.
