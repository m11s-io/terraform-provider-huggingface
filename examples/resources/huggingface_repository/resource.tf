resource "huggingface_repository" "example" {
  namespace       = "your-hf-namespace"
  name            = "example-model"
  private         = true
  deletion_policy = "retain"
}
