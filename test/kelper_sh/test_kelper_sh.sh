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
