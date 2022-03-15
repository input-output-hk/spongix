#!/usr/bin/env bash

set -exuo pipefail

urls=(
)

mkdir -p narinfo

for url in ${urls[*]}; do
  curl -s "http://127.0.0.1:7777/$url" > "narinfo/$url"
done

rm -f /tmp/spongix/index/nar/1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar
rm -rf /tmp/spongix/store/*
nix store dump-path /nix/store/cbckczjas96g9smn1g2s9kr8m18yg1pb-pdftk-3.2.1 > 1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar
curl -s -v http://127.0.0.1:7777/nar/1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar > 1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar.0
curl -s -v http://127.0.0.1:7777/nar/1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar > 1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar.1
curl -s -v http://127.0.0.1:7777/nar/1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar > 1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar.2
sha256sum 1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar*
