#!/usr/bin/env bash
#
# kelper.sh - single-file bash edition of kelper, a kubectl wrapper.
#
# For machines where the kelper binary cannot be installed: copy this file,
# `chmod +x kelper.sh`, and `alias k=./kelper.sh` (or put it on your PATH).
#
# Needs: bash 4+, kubectl and jq 1.6+. `kubeconfig` also needs openssl.
#
#   kelper.sh get <res> -o yaml        cleaned YAML; Secrets are decoded
#   kelper.sh images|resources|volumes [pod] [-n ns | -A] [-l selector]
#   kelper.sh healthcheck [-n ns | -A] [--severity warn] [--watch]
#   kelper.sh kubeconfig [readonly|admin|scoped] [--user u] [-n ns] [-o file]
#   kelper.sh <anything else>          forwarded to kubectl unchanged
#
# A `get -o yaml` of a list that mixes Secrets with other kinds shows only the
# decoded Secrets.
#
# Not in this edition (use the Go binary): pull, embedded k9s, the live
# dashboard, api-server failover, shell completion and the deeper checks
# (resource limits, volume usage, certificates, custom resources).

KELPER_SH_VERSION="1.0.0"

if [ -z "${BASH_VERSINFO:-}" ] || [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
  echo "error: kelper.sh needs bash 4 or newer (found ${BASH_VERSION:-unknown}); on macOS: brew install bash" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then COLOR=1; else COLOR=0; fi
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then ECOLOR=1; else ECOLOR=0; fi

paint() { # paint <sgr> <text...>
  local code=$1; shift
  if [ "$COLOR" = 1 ]; then printf '\033[%sm%s\033[0m' "$code" "$*"; else printf '%s' "$*"; fi
}

die() {
  if [ "$ECOLOR" = 1 ]; then printf '\033[31merror: %s\033[0m\n' "$*" >&2; else printf 'error: %s\n' "$*" >&2; fi
  exit 1
}

info() { printf '%s\n' "$*" >&2; }

need_jq() {
  command -v jq >/dev/null 2>&1 || die "jq is required for this command (https://jqlang.github.io/jq/)"
}

# Flags given before the command (--kubeconfig, --context, -n, ...) apply to
# every kubectl call this script makes.
GLOBAL=()
kc() { command kubectl "${GLOBAL[@]}" "$@"; }

# kubectl global flags whose value may be a separate argument. Needed to find
# the real command in `kelper.sh -n kube-system get pods`.
VALUE_FLAGS=" --kubeconfig -n --namespace --context --cluster --user -s --server --token --as --as-group --as-uid --certificate-authority --client-certificate --client-key --tls-server-name --request-timeout --cache-dir --password --username --profile --profile-output -v --v --vmodule --log-file --log-dir "
is_value_flag() { [[ "$VALUE_FLAGS" == *" $1 "* ]]; }

# Parse one flag shared by the native commands. Sets FLAG_CONSUMED to the number
# of arguments used (0 when $1 is not one of them).
NS="" ALLNS=0 SELECTOR="" FLAG_CONSUMED=0
parse_common_flag() { # parse_common_flag <arg> <next-arg>
  FLAG_CONSUMED=0
  case $1 in
    -n|--namespace) NS=$2; FLAG_CONSUMED=2 ;;
    -n=*|--namespace=*) NS=${1#*=}; FLAG_CONSUMED=1 ;;
    -n?*) NS=${1#-n}; FLAG_CONSUMED=1 ;;
    -A|--all-namespaces) ALLNS=1; FLAG_CONSUMED=1 ;;
    -l|--selector) SELECTOR=$2; FLAG_CONSUMED=2 ;;
    --selector=*) SELECTOR=${1#*=}; FLAG_CONSUMED=1 ;;
    -l?*) SELECTOR=${1#-l}; FLAG_CONSUMED=1 ;;
    --kubeconfig|--context) GLOBAL+=("$1" "$2"); FLAG_CONSUMED=2 ;;
    --kubeconfig=*|--context=*) GLOBAL+=("$1"); FLAG_CONSUMED=1 ;;
  esac
}

usage() {
  cat <<EOF
kelper.sh $KELPER_SH_VERSION - kubectl wrapper (bash edition)

Usage: kelper.sh [kubectl global flags] <command> [args]

Commands:
  get <res> -o yaml     cleaned YAML (server noise removed); Secrets are decoded.
                        With -o yaml, --raw gives plain kubectl output.
  healthcheck, health   diagnose pods, workloads, nodes, volumes and services
                        (-n ns | -A, --severity info|warn|error, --watch [--interval s])
  images, image, imgs   container images per pod
  resources, res        resource requests and limits per pod
  volumes, vols         volume mounts and pod volumes per pod
  kubeconfig            create a kubeconfig for a new user (CSR + RBAC);
                        readonly | admin | scoped, or run bare for a prompt wizard
  help                  this text

Pod inspectors take [pod] [-n ns | -A] [-l selector]. Anything else is passed to
kubectl as is, so alias k=kelper.sh is safe.
EOF
}

# ---------------------------------------------------------------------------
# jq library: YAML emitter and shared pieces
# ---------------------------------------------------------------------------

JQ_LIB=$(cat <<'EOF'
def paint($c): if $color == 1 then "\u001b[\($c)m" + . + "\u001b[0m" else . end;
def isscalar: type != "object" and type != "array";
def isempty: (type == "object" or type == "array") and length == 0;
def ymlkey: if test("^[A-Za-z_][A-Za-z0-9_./-]*$") then . else @json end;
def plain_ok:
  (test("^[A-Za-z0-9_./($][^\u0001-\u001f]*$") or test("^-[A-Za-z0-9_./$()-][^\u0001-\u001f]*$"))
  and (test("[ :]$") | not)
  and (test(": | #") | not)
  and (test("^\\.[0-9]") | not)
  and (test("^\\.(inf|nan)$"; "i") | not)
  and (test("^[0-9]{4}-[0-9]{2}-[0-9]{2}") | not)
  and (test("^[0-9][0-9_]*(\\.[0-9_]*)?([eE][-+]?[0-9]+)?$") | not)
  and (test("^[0-9]+(:[0-9]+)+(\\.[0-9]*)?$") | not)
  and (test("^0[xXoObB]") | not)
  and (test("^(true|false|null|yes|no|on|off|y|n)$"; "i") | not);
def scalar:
  if . == null then "null"
  elif type == "string" then (if plain_ok then . else @json end)
  else tostring end;
def multiline:
  type == "string" and contains("\n")
  and (test("^[ \t\n]") | not) and (test("[ \t]\n|\t") | not)
  and (test("\n\n+$") | not) and (test("[\u0001-\u0008\u000b-\u001f]") | not);
# YAML lines for any value, as an array of strings indented by $ind.
def blk($ind):
  if type == "object" then
    [ to_entries[] | (.key | ymlkey) as $k | .value as $v
      | if ($v | isscalar) then
          if ($v | multiline) then
            ($ind + $k + ": " + ($v | if endswith("\n") then "|" else "|-" end)),
            ($v | rtrimstr("\n") | split("\n")[] | if . == "" then "" else $ind + "  " + . end)
          else $ind + $k + ": " + ($v | scalar) end
        elif ($v | isempty) then $ind + $k + ": " + (if ($v | type) == "array" then "[]" else "{}" end)
        else ($ind + $k + ":"), (($v | if ($v | type) == "array" then blk($ind) else blk($ind + "  ") end)[]) end ]
  else
    [ .[] | . as $v
      | if ($v | isscalar) then
          $ind + "- " + ($v | if type == "string" and contains("\n") then @json else scalar end)
        elif ($v | isempty) then $ind + "- " + (if ($v | type) == "array" then "[]" else "{}" end)
        else ($v | blk($ind + "  ")) as $l
          | ($ind + "- " + ($l[0] | .[($ind | length) + 2:])), $l[1:][] end ]
  end;
def yaml: blk("")[];
EOF
)

# `get -o yaml`: decode Secrets, otherwise strip server-populated fields.
JQ_GET=$(cat <<'EOF'
def generic: [["metadata","uid"],["metadata","resourceVersion"],["metadata","creationTimestamp"],
  ["metadata","generation"],["metadata","ownerReferences"],["metadata","generateName"],
  ["metadata","finalizers"],["metadata","managedFields"],["status"]];
def tpl: [["spec","template","metadata","creationTimestamp"],
  ["spec","template","spec","terminationGracePeriodSeconds"],["spec","template","spec","dnsPolicy"],
  ["spec","template","spec","restartPolicy"],["spec","template","spec","schedulerName"]];
def bykind: {
  Deployment: ([["spec","progressDeadlineSeconds"],["spec","revisionHistoryLimit"]] + tpl),
  StatefulSet: tpl,
  DaemonSet: tpl,
  Pod: [["spec","terminationGracePeriodSeconds"],["spec","dnsPolicy"],["spec","restartPolicy"],
    ["spec","schedulerName"],["spec","nodeName"],["spec","serviceAccountName"],
    ["spec","enableServiceLinks"],["spec","preemptionPolicy"],["spec","priority"]],
  Service: [["spec","clusterIP"],["spec","clusterIPs"],["spec","internalTrafficPolicy"],
    ["spec","ipFamilies"],["spec","ipFamilyPolicy"],["spec","sessionAffinity"]]
};
def clean1:
  (.kind // "") as $k
  | delpaths(generic + (bykind[$k] // []))
  | (.spec.containers[]?, .spec.initContainers[]?,
     .spec.template.spec.containers[]?, .spec.template.spec.initContainers[]?)
    |= del(.terminationMessagePath, .terminationMessagePolicy);
def clean:
  if .kind == "List" then .items |= map(clean1)
  else clean1 end;
def has_secret: .kind == "Secret" or (.kind == "List" and any(.items[]?; .kind == "Secret"));
def secrets: if .kind == "List" then .items[] | select(.kind == "Secret") else . end;
def decoded: (try @base64d catch "(decode error)");
def show_secrets:
  (.kind == "List") as $many
  | secrets
  | (if $many then ("secret: \(.metadata.name) (-n \(.metadata.namespace))" | paint("1;38;5;33")), "" else empty end),
    ((.data // {}) | to_entries[]
      | ("KEY: " + .key | paint("1;38;5;36")), ("─" * 25 | paint("2")), (.value | decoded), "");
if has_secret then show_secrets else (clean | yaml) end
EOF
)

# images / resources / volumes: one program, picked with $mode.
JQ_PODS=$(cat <<'EOF'
def podsof: if .kind == "List" then .items[] else . end;
def header:
  "pod: \(.metadata.name) (-n \(.metadata.namespace))" as $h
  | ($h | paint("1;38;5;33")), ("─" * ($h | length) | paint("2")), "";
def group($name; f):
  "  \($name):",
  (if ((. // []) | length) == 0 then "    (none)" else (.[] | "    \(.name):", f) end),
  "";
def res:
  if ((.resources.limits // {}) | length) == 0 and ((.resources.requests // {}) | length) == 0
  then "      (none)" else (.resources | blk("      ")[]) end;
def mounts:
  if ((.volumeMounts // []) | length) == 0 then "      (none)" else (.volumeMounts | blk("      ")[]) end;
def img: "      image: \(.image)";
def per: if $mode == "images" then img elif $mode == "resources" then res else mounts end;
podsof
| header,
  (.spec.initContainers | group("initContainers"; per)),
  (.spec.containers | group("containers"; per)),
  (if $mode == "volumes" then
     "  shared volumes for \(.metadata.name) pod:",
     (if ((.spec.volumes // []) | length) == 0 then "    (none)" else (.spec.volumes | blk("    ")[]) end),
     ""
   else empty end)
EOF
)

# healthcheck: findings as tab-separated rows, worst first.
JQ_HEALTH=$(cat <<'EOF'
def row($sev; $ns; $kind; $name; $reason; $msg):
  {sev: $sev, ns: ($ns // ""), kind: $kind, name: $name, reason: $reason, msg: $msg};
def isin($list): . as $x | ($list | index($x)) != null;
def secs($t): try (now - ($t | fromdateiso8601)) catch 1e9;
def ago($s): if $s < 120 then "\($s | floor)s" elif $s < 7200 then "\($s / 60 | floor)m" else "\($s / 3600 | floor)h" end;
def bad_waiting: ["CrashLoopBackOff","ImagePullBackOff","ErrImagePull","ErrImageNeverPull","InvalidImageName",
  "CreateContainerConfigError","CreateContainerError","RunContainerError"];

def pod_findings:
  ($pods[0].items // [])[] | . as $p | .metadata as $m | .status as $s
  | def r($sev; $reason; $msg): row($sev; $m.namespace; "Pod"; $m.name; $reason; $msg);
  if $s.phase == "Succeeded" then empty else
    ( if $s.phase == "Failed" then r("ERROR"; ($s.reason // "Failed"); ($s.message // "pod failed")) else empty end,
      ( if $s.phase == "Pending" and ((($s.containerStatuses // []) + ($s.initContainerStatuses // [])) | length) == 0 then
          ( [$s.conditions[]? | select(.type == "PodScheduled" and .status == "False")][0] ) as $c
          | if $c then r("ERROR"; ($c.reason // "Unschedulable"); "cannot be scheduled: \($c.message // "")")
            else r("WARN"; "Pending"; "pending, no containers started yet") end
        else empty end ),
      ( if $m.deletionTimestamp and secs($m.deletionTimestamp) > (($m.deletionGracePeriodSeconds // 30) + 300) then
          r("WARN"; "StuckTerminating"; "terminating for \(ago(secs($m.deletionTimestamp)))")
        else empty end ),
      ( if $s.phase == "Failed" then empty else
          ((($s.initContainerStatuses // []) | map(. + {init: true})) + ($s.containerStatuses // []))[]
          | . as $c | ($c.state.waiting.reason // "") as $w
          | (if $c.init then "init container" else "container" end) as $what
          | if ($w | isin(bad_waiting)) then r("ERROR"; $w; "\($what) \"\($c.name)\" is \($w)")
            elif ($c.state.terminated.reason == "OOMKilled") then r("ERROR"; "OOMKilled"; "\($what) \"\($c.name)\" was OOMKilled")
            elif ($c.lastState.terminated.reason == "OOMKilled") then
              r("ERROR"; "OOMKilled"; "\($what) \"\($c.name)\" was OOMKilled, restarted \($c.restartCount) time(s)")
            elif ($c.restartCount > 0 and secs($c.lastState.terminated.finishedAt // "") < 600) then
              r("WARN"; "RestartChurn"; "\($what) \"\($c.name)\" restarted \($c.restartCount) time(s), last \(ago(secs($c.lastState.terminated.finishedAt))) ago")
            elif ($s.phase == "Running" and $c.ready == false and ($c.state.terminated | not) and ($c.init | not)) then
              r("WARN"; "NotReady"; "container \"\($c.name)\" is running but not ready")
            else empty end
        end ) )
  end;

def workload_findings:
  ($workloads[0].items // [])[] | . as $w | .metadata as $m | .status as $s | .kind as $k
  | def r($sev; $reason; $msg): row($sev; $m.namespace; $k; $m.name; $reason; $msg);
  def short($want; $have):
    if $want > 0 and $have < $want
    then r((if $have == 0 then "ERROR" else "WARN" end); "ReplicasUnavailable"; "\($have)/\($want) replicas available")
    else empty end;
  if $k == "Deployment" then
    short(($w.spec.replicas // 1); ($s.availableReplicas // 0)),
    ( [$s.conditions[]? | select(.type == "Progressing" and .status == "False")][0] | select(.)
      | r("ERROR"; (.reason // "NotProgressing"); (.message // "rollout is not progressing")) )
  elif $k == "StatefulSet" then short(($w.spec.replicas // 1); ($s.readyReplicas // 0))
  elif $k == "DaemonSet" then short(($s.desiredNumberScheduled // 0); ($s.numberReady // 0))
  elif $k == "Job" then
    ( [$s.conditions[]? | select(.type == "Failed" and .status == "True")][0] | select(.)
      | select(secs(.lastTransitionTime) < 86400)
      | r("ERROR"; (.reason // "JobFailed"); (.message // "job failed")) )
  else empty end;

def node_findings:
  ($nodes[0].items // [])[] | .metadata.name as $n | .status.conditions as $c
  | def r($sev; $reason; $msg): row($sev; ""; "Node"; $n; $reason; $msg);
    ( ([$c[]? | select(.type == "Ready")][0]) as $rd
      | if $rd == null or $rd.status != "True" then r("ERROR"; "NodeNotReady"; ($rd.message // "node is not ready")) else empty end ),
    ( $c[]? | select((.type | isin(["MemoryPressure","DiskPressure","PIDPressure","NetworkUnavailable"])) and .status == "True")
      | r("WARN"; .type; (.message // .type)) ),
    ( if .spec.unschedulable then r("WARN"; "Cordoned"; "node is cordoned; no new pods will be scheduled") else empty end );

def pvc_findings:
  ($pvcs[0].items // [])[] | .metadata as $m | .status.phase as $ph
  | ([($pods[0].items // [])[] | select(.metadata.namespace == $m.namespace) | .spec.volumes[]?.persistentVolumeClaim.claimName]
     | index($m.name) != null) as $used
  | def r($sev; $reason; $msg): row($sev; $m.namespace; "PersistentVolumeClaim"; $m.name; $reason; $msg);
    if $ph == "Pending" then
      (if $used then r("ERROR"; "PVCPending"; "claim is unbound; pods using it cannot start")
       elif secs($m.creationTimestamp) > 300 then r("WARN"; "PVCPending"; "claim is unbound and unused") else empty end)
    elif $ph == "Lost" then r("ERROR"; "PVCLost"; "the bound volume is gone")
    else empty end;

def service_findings:
  ([($slices[0].items // [])[]
    | {k: "\(.metadata.namespace)/\(.metadata.labels["kubernetes.io/service-name"] // "")",
       n: ([.endpoints[]? | select(.conditions.ready != false)] | length)}]
   | group_by(.k) | map({key: .[0].k, value: (map(.n) | add)}) | from_entries) as $ready
  | ($services[0].items // [])[] | .metadata as $m
  | select(((.spec.selector // {}) | length) > 0 and .spec.type != "ExternalName")
  | select(($ready["\($m.namespace)/\($m.name)"] // 0) == 0)
  | row("ERROR"; $m.namespace; "Service"; $m.name; "NoReadyEndpoints"; "service has no ready endpoints; traffic to it will fail");

def rank: {"CRITICAL": 0, "ERROR": 1, "WARN": 2, "INFO": 3};
[pod_findings, workload_findings, node_findings, pvc_findings, service_findings]
| map(select(rank[.sev] <= rank[$min]))
| sort_by([rank[.sev], .ns, .kind, .name, .reason])
| .[] | [.sev, (if .ns == "" then "-" else .ns end), .kind, .name, .reason, .msg] | @tsv
EOF
)

# A new user's kubeconfig, as YAML.
JQ_KUBECONFIG=$(cat <<'EOF'
{apiVersion: "v1", kind: "Config",
  clusters: [{name: $cluster, cluster: ({server: $server}
    + (if $ca != "" then {"certificate-authority-data": $ca} else {} end)
    + (if $insecure then {"insecure-skip-tls-verify": true} else {} end)
    + (if $sni != "" then {"tls-server-name": $sni} else {} end))}],
  users: [{name: $user, user: {"client-certificate-data": $cert, "client-key-data": $key}}],
  contexts: [{name: ($user + "@" + $cluster), context: ({cluster: $cluster, user: $user}
    + (if $ns != "" then {namespace: $ns} else {} end))}],
  "current-context": ($user + "@" + $cluster),
  preferences: {}} | yaml
EOF
)

# ---------------------------------------------------------------------------
# get -o yaml
# ---------------------------------------------------------------------------

cmd_get() { # args after `get`
  local args=("$@") out=() yaml=0 raw=0 i a
  for ((i = 0; i < ${#args[@]}; i++)); do
    a=${args[i]}
    if [ "$a" = "--" ]; then out+=("${args[@]:i}"); break; fi
    case $a in
      --raw) raw=1; out+=("$a") ;;
      -o|--output)
        if [ "${args[i+1]:-}" = yaml ]; then yaml=1; out+=(-o json); i=$((i + 1)); else out+=("$a"); fi ;;
      -oyaml|-o=yaml|--output=yaml) yaml=1; out+=(-o json) ;;
      *) out+=("$a") ;;
    esac
  done

  if [ "$yaml" = 0 ]; then
    exec kubectl "${GLOBAL[@]}" get "$@"
  fi
  if [ "$raw" = 1 ]; then # kelper's own --raw: plain kubectl YAML (kubectl's --raw takes a path and has no -o yaml)
    local plain=() x
    for x in "$@"; do [ "$x" = "--raw" ] || plain+=("$x"); done
    exec kubectl "${GLOBAL[@]}" get "${plain[@]}"
  fi
  if ! command -v jq >/dev/null 2>&1; then
    info "kelper.sh: jq not found, showing plain kubectl output"
    exec kubectl "${GLOBAL[@]}" get "$@"
  fi

  local json rc
  json=$(kc get "${out[@]}"); rc=$?
  [ $rc -eq 0 ] || exit $rc
  printf '%s' "$json" | jq -r --argjson color "$COLOR" "$JQ_LIB
$JQ_GET" || exit 1
}

# ---------------------------------------------------------------------------
# images / resources / volumes
# ---------------------------------------------------------------------------

cmd_pods() { # cmd_pods <mode> [pod] [flags]
  local mode=$1; shift
  local pod="" a
  need_jq
  while [ $# -gt 0 ]; do
    a=$1
    parse_common_flag "$a" "${2:-}"
    if [ "$FLAG_CONSUMED" -gt 0 ]; then
      [ "$FLAG_CONSUMED" -le $# ] || die "$a needs a value"
      shift "$FLAG_CONSUMED"; continue
    fi
    case $a in
      -h|--help) echo "usage: kelper.sh $mode [pod] [-n ns | -A] [-l selector]"; exit 0 ;;
      -*) die "unknown flag $a" ;;
      *) [ -z "$pod" ] || die "only one pod name allowed"; pod=$a ;;
    esac
    shift
  done

  local scope=()
  if [ "$ALLNS" = 1 ]; then scope+=(-A); elif [ -n "$NS" ]; then scope+=(-n "$NS"); fi
  [ -z "$SELECTOR" ] || scope+=(-l "$SELECTOR")

  local json rc
  json=$(kc get pods ${pod:+"$pod"} "${scope[@]}" -o json); rc=$?
  [ $rc -eq 0 ] || exit $rc
  printf '%s' "$json" | jq -r --argjson color "$COLOR" --arg mode "$mode" "$JQ_LIB
$JQ_PODS"
}

# ---------------------------------------------------------------------------
# healthcheck
# ---------------------------------------------------------------------------

HC_MIN=info
hc_fetch() { # hc_fetch <name> <resources> [kubectl flags...]; JSON into $T/<name>.json
  local name=$1 res=$2; shift 2
  if kc get "$res" "$@" -o json >"$T/$name.json" 2>"$T/$name.err"; then
    FETCHED=$((FETCHED + 1))
  else
    local why
    why=$(head -n1 "$T/$name.err" | sed -e 's/^[Ee]rror from server //' -e 's/^[Ee]rror: //' | cut -c1-90)
    DEGRADED+=("$name ($why)")
    echo '{"items":[]}' >"$T/$name.json"
  fi
}

hc_once() {
  local scope=()
  if [ -n "$HC_NS" ] && [ "$ALLNS" = 0 ]; then scope=(-n "$HC_NS"); else scope=(-A); fi
  FETCHED=0 DEGRADED=()

  hc_fetch pods pods "${scope[@]}"
  hc_fetch workloads deployments,statefulsets,daemonsets,jobs "${scope[@]}"
  hc_fetch pvcs persistentvolumeclaims "${scope[@]}"
  hc_fetch services services "${scope[@]}"
  hc_fetch slices endpointslices "${scope[@]}"
  if [ "${scope[0]}" = -A ]; then hc_fetch nodes nodes; else echo '{"items":[]}' >"$T/nodes.json"; fi

  if [ "$FETCHED" -eq 0 ]; then
    die "could not read anything from the cluster: ${DEGRADED[0]:-unreachable}"
  fi

  local rows
  rows=$(jq -nr --arg min "$(printf '%s' "$HC_MIN" | tr '[:lower:]' '[:upper:]')" \
    --slurpfile pods "$T/pods.json" --slurpfile workloads "$T/workloads.json" \
    --slurpfile pvcs "$T/pvcs.json" --slurpfile services "$T/services.json" \
    --slurpfile slices "$T/slices.json" --slurpfile nodes "$T/nodes.json" \
    "$JQ_HEALTH") || die "healthcheck failed to evaluate cluster state"

  local sev count total_bad=0 summary=() tab
  tab=$(printf '\t')
  for sev in CRITICAL ERROR WARN INFO; do
    count=$(printf '%s\n' "$rows" | awk -F'\t' -v s="$sev" '$1 == s { n++ } END { print n + 0 }')
    [ "$count" -gt 0 ] || continue
    case $sev in
      CRITICAL|ERROR) paint "1;31" "$sev ($count)" ;;
      WARN) paint "33" "$sev ($count)" ;;
      *) paint "2" "$sev ($count)" ;;
    esac
    printf '\n'
    { printf 'NAMESPACE\tKIND\tNAME\tREASON\tMESSAGE\n'
      printf '%s\n' "$rows" | awk -F'\t' -v s="$sev" '$1 == s' | cut -f2-
    } | column -t -s "$tab" | sed 's/^/ /'
    printf '\n'
    summary+=("$count $sev")
    case $sev in INFO) ;; *) total_bad=$((total_bad + count)) ;; esac
  done

  if [ "${#summary[@]}" -eq 0 ]; then
    paint "32" "No health problems found."; printf '\n'
  else
    local joined=${summary[0]} i
    for ((i = 1; i < ${#summary[@]}; i++)); do joined+=", ${summary[i]}"; done
    printf 'Summary: %s\n' "$joined"
  fi
  if [ "${#DEGRADED[@]}" -gt 0 ]; then
    local joined_d=${DEGRADED[0]} j
    for ((j = 1; j < ${#DEGRADED[@]}; j++)); do joined_d+=", ${DEGRADED[j]}"; done
    printf 'Degraded: %s\n' "$joined_d"
  fi
  HC_BAD=$total_bad
}

cmd_health() {
  need_jq
  HC_NS="" HC_BAD=0
  local watch=0 interval=10 a
  while [ $# -gt 0 ]; do
    a=$1
    parse_common_flag "$a" "${2:-}"
    if [ "$FLAG_CONSUMED" -gt 0 ]; then
      [ "$FLAG_CONSUMED" -le $# ] || die "$a needs a value"
      shift "$FLAG_CONSUMED"; continue
    fi
    case $a in
      --severity) HC_MIN=${2:-}; shift ;;
      --severity=*) HC_MIN=${a#*=} ;;
      -w|--watch) watch=1 ;;
      --interval) interval=${2:-}; shift ;;
      --interval=*) interval=${a#*=} ;;
      -h|--help)
        echo "usage: kelper.sh healthcheck [-n ns | -A] [--severity info|warn|error] [--watch [--interval seconds]]"
        echo "default scope is every namespace plus nodes; exits 1 when anything at warn or above is found"
        exit 0 ;;
      *) die "unknown argument $a" ;;
    esac
    shift
  done
  HC_NS=$NS
  case $(printf '%s' "$HC_MIN" | tr '[:upper:]' '[:lower:]') in
    info|warn|error|critical) ;;
    *) die "unknown severity \"$HC_MIN\" (use info, warn or error)" ;;
  esac
  { [[ $interval =~ ^[0-9]+$ ]] && [ "$interval" -gt 0 ]; } || die "--interval must be a number of seconds"

  T=$(mktemp -d) || die "cannot create a temporary directory"
  trap 'rm -rf "$T"' EXIT

  if [ "$watch" = 1 ]; then
    trap 'rm -rf "$T"; exit 0' INT TERM
    local out
    while true; do
      out=$(hc_once 2>&1)
      printf '\033[H\033[2J%s\n\n%s (every %ss, ctrl-c to stop)\n' "$out" "$(date '+%H:%M:%S')" "$interval"
      sleep "$interval"
    done
  fi
  hc_once
  [ "$HC_BAD" -eq 0 ] || exit 1
}

# ---------------------------------------------------------------------------
# kubeconfig
# ---------------------------------------------------------------------------

csv_json() { # "a, b" -> ["a","b"]; "core" means the core API group
  jq -nc --arg s "$1" '$s | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0)) | map(if . == "core" then "" else . end)'
}

kc_prompt() { # kc_prompt <var> <question> [default]
  local ans
  read -r -p "$2${3:+ [$3]}: " ans || die "no input"
  printf -v "$1" '%s' "${ans:-${3:-}}"
}

kc_wizard() {
  local n
  echo "Create a kubeconfig for a new user (ctrl-c to cancel)" >&2
  echo "  1) readonly - get/list/watch on everything" >&2
  echo "  2) admin    - full access" >&2
  echo "  3) scoped   - choose API groups, resources and verbs" >&2
  kc_prompt n "Account type" 1
  case $n in 1|readonly) KC_TYPE="readonly" ;; 2|admin) KC_TYPE="admin" ;; 3|scoped) KC_TYPE="scoped" ;; *) die "pick 1, 2 or 3" ;; esac
  kc_prompt KC_USER "Username (blank to generate)"
  kc_prompt NS "Namespace (blank for cluster-wide)"
  if [ "$KC_TYPE" = scoped ]; then
    kc_prompt KC_APIGROUPS "API groups (comma separated, core for the core group)" "*"
    kc_prompt KC_RESOURCES "Resources (comma separated)"
    kc_prompt KC_VERBS "Verbs (comma separated)"
  fi
  kc_prompt KC_OUT "Output file (blank for stdout)"
}

cmd_kubeconfig() {
  need_jq
  KC_TYPE="" KC_USER="" KC_OUT="" KC_RESOURCES="" KC_VERBS="" KC_APIGROUPS="*"
  local yes=0 a
  if [ $# -eq 0 ]; then
    kc_wizard
  else
    while [ $# -gt 0 ]; do
      a=$1
      parse_common_flag "$a" "${2:-}"
      if [ "$FLAG_CONSUMED" -gt 0 ]; then
        [ "$FLAG_CONSUMED" -le $# ] || die "$a needs a value"
        shift "$FLAG_CONSUMED"; continue
      fi
      case $a in
        readonly|admin|scoped) KC_TYPE=$a ;;
        --user) KC_USER=${2:-}; shift ;;
        --user=*) KC_USER=${a#*=} ;;
        -o|--output) KC_OUT=${2:-}; shift ;;
        --output=*) KC_OUT=${a#*=} ;;
        --resources) KC_RESOURCES=${2:-}; shift ;;
        --resources=*) KC_RESOURCES=${a#*=} ;;
        --verbs) KC_VERBS=${2:-}; shift ;;
        --verbs=*) KC_VERBS=${a#*=} ;;
        --apigroups) KC_APIGROUPS=${2:-}; shift ;;
        --apigroups=*) KC_APIGROUPS=${a#*=} ;;
        -y|--yes) yes=1 ;;
        -h|--help)
          cat <<EOF
usage: kelper.sh kubeconfig [readonly|admin|scoped] [--user name] [-n namespace] [-o file] [-y]
       kelper.sh kubeconfig scoped --resources pods,configmaps --verbs get,list [--apigroups core,apps]
Accounts are cluster-wide unless -n is given. Run bare for a prompt wizard.
EOF
          exit 0 ;;
        *) die "unknown argument $a" ;;
      esac
      shift
    done
  fi

  [ -n "$KC_TYPE" ] || die "account type required: readonly, admin or scoped"
  if [ "$KC_TYPE" = scoped ]; then
    [ -n "$KC_RESOURCES" ] || die "scoped accounts need --resources"
    [ -n "$KC_VERBS" ] || die "scoped accounts need --verbs"
  fi
  if [ -n "$KC_USER" ]; then
    [[ $KC_USER =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] && [ ${#KC_USER} -le 63 ] \
      || die "username \"$KC_USER\" must be lowercase letters, digits, - or . (RFC 1123, max 63)"
  fi
  command -v openssl >/dev/null 2>&1 || die "openssl is required to create a user key and certificate request"
  if [ -z "$KC_USER" ]; then KC_USER="$KC_TYPE-$(openssl rand -hex 4)"; fi

  local rules
  case $KC_TYPE in
    readonly) rules='[{"apiGroups":["*"],"resources":["*"],"verbs":["get","list","watch"]}]' ;;
    admin) rules='[{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}]' ;;
    scoped) rules=$(jq -nc --argjson g "$(csv_json "$KC_APIGROUPS")" --argjson r "$(csv_json "$KC_RESOURCES")" --argjson v "$(csv_json "$KC_VERBS")" \
              '[{apiGroups: $g, resources: $r, verbs: $v}]') ;;
  esac

  # Everything the new user is built from, read before anything is changed.
  local cfg server ca_data ca_file cluster_name insecure sni
  cfg=$(kc config view --minify --raw -o json) || die "could not read the current kubeconfig context"
  cluster_name=$(jq -r '.clusters[0].name // empty' <<<"$cfg")
  server=$(jq -r '.clusters[0].cluster.server // empty' <<<"$cfg")
  ca_data=$(jq -r '.clusters[0].cluster["certificate-authority-data"] // empty' <<<"$cfg")
  ca_file=$(jq -r '.clusters[0].cluster["certificate-authority"] // empty' <<<"$cfg")
  insecure=$(jq -r '.clusters[0].cluster["insecure-skip-tls-verify"] // false' <<<"$cfg")
  sni=$(jq -r '.clusters[0].cluster["tls-server-name"] // empty' <<<"$cfg")
  [ -n "$server" ] || die "the current context has no cluster server"
  if [ -z "$ca_data" ] && [ -n "$ca_file" ]; then ca_data=$(base64 <"$ca_file" | tr -d '\n'); fi

  local scope_desc="cluster-wide"
  [ -z "$NS" ] || scope_desc="namespace $NS"
  info "kelper will create on cluster $cluster_name ($server):"
  info "  - CertificateSigningRequest ${KC_USER}-kelper-csr, approved"
  if [ -z "$NS" ]; then
    info "  - ClusterRole ${KC_USER}-kelper-role and ClusterRoleBinding ${KC_USER}-kelper-binding"
  else
    info "  - Role ${KC_USER}-kelper-role and RoleBinding ${KC_USER}-kelper-binding in $NS"
  fi
  info "  type: $KC_TYPE  user: $KC_USER  scope: $scope_desc"
  info "  rules: $(jq -c . <<<"$rules")"
  if [ "$yes" = 0 ]; then
    local ans
    read -r -p "Continue? [y/N]: " ans || ans=n
    case $ans in y|Y|yes|YES) ;; *) info "Cancelled."; exit 0 ;; esac
  fi

  local key csr_pem csr_name=${KC_USER}-kelper-csr
  key=$(openssl genrsa 2048 2>/dev/null) || die "openssl could not generate a key"
  csr_pem=$(openssl req -new -key <(printf '%s\n' "$key") -subj "/CN=$KC_USER/O=kelper" 2>/dev/null) \
    || die "openssl could not create the certificate request"

  kc delete csr "$csr_name" --ignore-not-found >&2 || die "could not remove an old request"
  jq -n --arg name "$csr_name" --arg req "$(printf '%s\n' "$csr_pem" | base64 | tr -d '\n')" \
    '{apiVersion: "certificates.k8s.io/v1", kind: "CertificateSigningRequest", metadata: {name: $name},
      spec: {request: $req, signerName: "kubernetes.io/kube-apiserver-client", usages: ["client auth"]}}' \
    | kc apply -f - >&2 || die "could not submit the certificate request"
  kc certificate approve "$csr_name" >&2 || die "could not approve the certificate request"

  local cert="" i
  for ((i = 0; i < 30; i++)); do
    cert=$(kc get csr "$csr_name" -o jsonpath='{.status.certificate}' 2>/dev/null)
    [ -z "$cert" ] || break
    sleep 1
  done
  [ -n "$cert" ] || die "the certificate was not issued within 30s (is the cluster's signer enabled?)"

  local role_name=${KC_USER}-kelper-role bind_name=${KC_USER}-kelper-binding
  if [ -z "$NS" ]; then
    jq -n --arg n "$role_name" --argjson r "$rules" '{apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRole", metadata: {name: $n}, rules: $r}' \
      | kc apply -f - >&2 || die "could not create the ClusterRole"
    kc delete clusterrolebinding "$bind_name" --ignore-not-found >&2
    jq -n --arg n "$bind_name" --arg r "$role_name" --arg u "$KC_USER" \
      '{apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRoleBinding", metadata: {name: $n},
        roleRef: {apiGroup: "rbac.authorization.k8s.io", kind: "ClusterRole", name: $r},
        subjects: [{apiGroup: "rbac.authorization.k8s.io", kind: "User", name: $u}]}' \
      | kc apply -f - >&2 || die "could not create the ClusterRoleBinding"
  else
    jq -n --arg n "$role_name" --arg ns "$NS" --argjson r "$rules" \
      '{apiVersion: "rbac.authorization.k8s.io/v1", kind: "Role", metadata: {name: $n, namespace: $ns}, rules: $r}' \
      | kc apply -f - >&2 || die "could not create the Role"
    kc delete rolebinding "$bind_name" -n "$NS" --ignore-not-found >&2
    jq -n --arg n "$bind_name" --arg ns "$NS" --arg r "$role_name" --arg u "$KC_USER" \
      '{apiVersion: "rbac.authorization.k8s.io/v1", kind: "RoleBinding", metadata: {name: $n, namespace: $ns},
        roleRef: {apiGroup: "rbac.authorization.k8s.io", kind: "Role", name: $r},
        subjects: [{apiGroup: "rbac.authorization.k8s.io", kind: "User", name: $u}]}' \
      | kc apply -f - >&2 || die "could not create the RoleBinding"
  fi

  local out
  out=$(jq -nr --arg cluster "$cluster_name" --arg server "$server" --arg ca "$ca_data" --arg sni "$sni" \
      --argjson insecure "$insecure" --arg user "$KC_USER" --arg ns "$NS" --arg cert "$cert" \
      --arg key "$(printf '%s\n' "$key" | base64 | tr -d '\n')" --argjson color 0 \
      "$JQ_LIB
$JQ_KUBECONFIG") || die "could not build the kubeconfig"

  if [ -n "$KC_OUT" ]; then
    ( umask 077; printf '%s\n' "$out" >"$KC_OUT" ) || die "could not write $KC_OUT"
    info "Wrote kubeconfig for $KC_USER to $KC_OUT"
  else
    printf '%s\n' "$out"
  fi
}

# ---------------------------------------------------------------------------
# entry point
# ---------------------------------------------------------------------------

main() {
  local args=("$@") i a first=""
  local idx=${#args[@]}
  if [ ${#args[@]} -eq 0 ]; then usage; exit 0; fi
  case ${args[0]} in
    -v|--version) echo "kelper.sh $KELPER_SH_VERSION"; exit 0 ;;
    -h|--help|help) usage; exit 0 ;;
  esac

  for ((i = 0; i < ${#args[@]}; i++)); do
    a=${args[i]}
    if [[ $a != -* ]]; then first=$a; idx=$i; break; fi
    if is_value_flag "$a"; then i=$((i + 1)); fi
  done
  [ -n "$first" ] || exec kubectl "$@"

  GLOBAL=("${args[@]:0:idx}")
  local rest=("${args[@]:idx+1}")
  case $first in
    get) cmd_get "${rest[@]}" ;;
    healthcheck|health) cmd_health "${rest[@]}" ;;
    images|image|imgs|img) cmd_pods images "${rest[@]}" ;;
    resources|resource|res) cmd_pods resources "${rest[@]}" ;;
    volumes|volume|vols|vol) cmd_pods volumes "${rest[@]}" ;;
    kubeconfig) cmd_kubeconfig "${rest[@]}" ;;
    *) exec kubectl "$@" ;;
  esac
}

main "$@"
