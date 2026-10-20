#!/usr/bin/env bash
# Offline tests for kelper.sh: a stub kubectl answers from fixtures/, and output
# is compared with the files in golden/. Run `UPDATE=1 ./test_kelper_sh.sh` to
# regenerate the goldens after an intended output change, then review the diff.
set -u
here=$(cd "$(dirname "$0")" && pwd)
script="$here/../../kelper.sh"
export PATH="$here:$PATH" NO_COLOR=1
mkdir -p "$here/golden"
fail=0

check() { # check <name> <command...>; compares stdout with golden/<name>.txt
  local name=$1; shift
  local out; out=$("$@" 2>&1)
  if [ "${UPDATE:-}" = 1 ]; then printf '%s\n' "$out" >"$here/golden/$name.txt"; return; fi
  if diff <(printf '%s\n' "$out") "$here/golden/$name.txt" >/dev/null; then
    echo "ok   $name"
  else
    echo "FAIL $name"; diff <(printf '%s\n' "$out") "$here/golden/$name.txt" | head -20; fail=1
  fi
}

expect() { # expect <name> <want-exit> <want-substring> <command...>
  local name=$1 want=$2 sub=$3; shift 3
  local out rc; out=$("$@" 2>&1); rc=$?
  if [ "$rc" = "$want" ] && [[ $out == *"$sub"* ]]; then echo "ok   $name"
  else echo "FAIL $name (exit $rc, want $want; looking for: $sub)"; printf '%s\n' "$out" | head -10; fail=1; fi
}

command -v jq >/dev/null || { echo "jq is required for these tests"; exit 1; }

check get-deploy-yaml   "$script" get deploy web -o yaml
check get-deploy-oyaml  "$script" get deploy web -oyaml
check get-secret-decode "$script" get secret db -o yaml
check images            "$script" images
check resources         "$script" resources
check volumes           "$script" volumes
check health-broken     "$script" healthcheck
check health-errors     "$script" healthcheck --severity error

export STUB_ALL=1
check all-everything     "$script" all -A -y
check all-bare-piped     "$script" all
check all-namespaced     "$script" all -n prod
check all-names          "$script" all -A -y -o name
check all-names-ns       "$script" all -n prod -o name
check all-yaml           "$script" all -n prod -o yaml
check all-json           "$script" all -n prod -o json
check all-include-noisy  "$script" all -n prod --include-noisy --include ev,po
check all-exclude        "$script" all -A -y --exclude po,nodes
check all-leading-ns     "$script" -n prod all
expect all-forbidden-skipped 0 "Skipped: secrets (secrets is forbidden" "$script" all -A -y
expect all-empty 0 "No resources found in empty namespace." "$script" all -n empty --include deploy
expect all-bad-output 1 "unsupported output" "$script" all -n prod -o bogus
expect all-no-match 1 "no matching resource types" "$script" all -n prod --include nothing-like-this
expect all-cancelled-without-tty-still-runs 0 "pods (2)" "$script" all < /dev/null
unset STUB_ALL

expect health-exit-broken 1 "Summary:" "$script" healthcheck
STUB_PODS=healthy-pods STUB_WORKLOADS=empty expect health-exit-clean 0 "No health problems found." "$script" healthcheck
expect passthrough 0 "KUBECTL describe pod x" "$script" describe pod x
expect passthrough-leading-flags 0 "KUBECTL -n kube-system describe pod x" "$script" -n kube-system describe pod x
expect get-not-yaml 0 "KUBECTL get configmaps -o wide" "$script" get configmaps -o wide
expect get-raw 0 '"kind":"Deployment"' "$script" get deploy web -o yaml --raw
expect bad-severity 1 "unknown severity" "$script" healthcheck --severity loud
expect kubeconfig-needs-type 1 "account type required" "$script" kubeconfig -n demo
expect kubeconfig-bad-user 1 "RFC 1123" "$script" kubeconfig readonly --user Bad_Name -y

[ "$fail" = 0 ] && echo "all passed"
exit "$fail"
