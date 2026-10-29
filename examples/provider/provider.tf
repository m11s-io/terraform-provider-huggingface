terraform {
  required_providers {
    huggingface = {
      source  = "registry.terraform.io/m11s-io/huggingface"
      version = "= 0.1.0" # Target release; not yet published.
    }
  }
}
# Supply HF_TOKEN through the environment, not committed configuration.
provider "huggingface" {}
