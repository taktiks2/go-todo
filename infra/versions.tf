terraform {
  # ローカルは nix-darwin 供給の 1.14.9（2026-08-16 時点。最新安定版は 1.15.8）。
  # nixpkgs の terraform は BUSL で unfree なので flake.nix には入れず、
  # system 供給のままにする。ここで守るのは下限だけ。
  required_version = ">= 1.14"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }

  # このバケットは Terraform では作れない。backend がバケットの存在を前提に
  # するため（鶏と卵）。#1 の scripts/bootstrap.sh が作っている。
  #
  # backend ブロックは変数を受け付けないので、バケット名だけは直書きになる。
  backend "gcs" {
    bucket = "taktiks2-go-todo-tfstate"
    prefix = "infra"
  }
}
