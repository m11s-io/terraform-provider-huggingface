resource "huggingface_trusted_publisher" "example" {
  repo_id           = huggingface_repository.example.repo_id
  github_repository = "your-github-owner/training"
  branch            = "main"
  workflow          = "publish.yml"
}
