terraform {
  required_providers {
    hakopod = {
      source = "hakopod/hakopod"
    }
  }
}

# url and api_key fall back to HAKOPOD_API_URL and HAKOPOD_API_KEY.
provider "hakopod" {
  url = "https://hakopod.example.com"
  # workspace = "ws_..." # Hakopod Cloud only; or HAKOPOD_WORKSPACE.
}
