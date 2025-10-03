#!/bin/bash

namespace="-n default"
newuser="newuser"
color='\033[31m'
nc='\033[0m'

# CLI Help
cli_help() {
  cli_name=${0##*/}

  echo "
Usage: $cli_name [command]
Flags:
  get                           runs kubectl command - auto cleans yaml output and decodes secrets
  health, healthcheck           runs healthcheck on namespace defined
  image, images                 lists all images used in a pod or namespace (or all namespaces, by pod)
  resource, resources           lists all limits and requests in a pod or namespace (or all namespaces, by pod)
  volume, volumes               lists all volumeMounts and volumes in a pod or namespace (or all namespaces, by pod)
  kubeconfig (EXPERIMENTAL)     creates a readonly kubeconfig - needs polishing and more documentation
"
exit 1
}


healthcheck() {

    echo -e "${color}Unhealthy pods:${nc}"
    kubectl get po $namespace --no-headers | grep -Pv '\s+([1-9]+[\d]*)\/\1\s+' | grep -v Completed

    echo -e "${color}Unhealthy applications (deploy,sts,rs,ds,job,cronjob):${nc}"
    # TODO figure out way to check rs,ds,cronjob as well
    kubectl get deploy,sts,job $namespace --no-headers | grep -Pv '\s+([1-9]+[\d]*)\/\1\s+'

    exit 0
}

decode() {
    # TODO handle lists
    # TODO use yq instead of grep
    if [[ $(kubectl $@ | grep "kind: Secret") ]]; then
        kubectl $@ | yq -r '.data | map_values(@base64d)' | sed 's/\\n/\n/g'
        exit 0
    fi
}

neat() {
    # TODO add support for lists
    kubectl "$@" | yq eval 'del(.metadata.creationTimestamp,
        .metadata.uid,
        .metadata.generation,
        .metadata.ownerReferences,
        .metadata.generateName,
        .metadata.finalizers,
        .spec.progressDeadlineSeconds,
        .spec.revisionHistoryLimit,
        .spec.template.metadata.creationTimestamp,
        .spec.template.spec.containers.[].livenessProbe,
        .spec.template.spec.containers.[].readinessProbe,
        .spec.template.spec.containers.[].terminationMessagePath,
        .spec.template.spec.containers.[].terminationMessagePolicy,
        .spec.template.spec.dnsPolicy,
        .spec.template.spec.restartPolicy,
        .spec.template.spec.schedulerName,
        .spec.template.spec.terminationGracePerionSeconds,
        .spec.clusterIP,
        .spec.clusterIPs,
        .spec.internalTrafficPolicy,
        .spec.ipFamilies,
        .spec.ipFamilyPolicy,
        .spec.sessionAffinity,
        .metadata.resourceVersion,
        .status)' -
}

get_images() {
    # list initContainers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.initContainers[*].name}')
    printf "\n  initContainers: \n"
    for i in $imgname; do
        img=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.initContainers[${count}].image}")
        printf "    ${i}: ${img}\n"
        count=$((count+1))
    done
    # list containers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.containers[*].name}')
    printf "\n  containers: \n"
    for i in $imgname; do
        img=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.containers[${count}].image}")
        printf "    ${i}: ${img}\n"
        count=$((count+1))
    done
    echo ""
}

get_volumes() {
    # list initContainers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.initContainers[*].name}')
    printf "\n  initContainers: \n"
    for i in $imgname; do
        vol=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.initContainers[${count}].volumeMounts}" | yq -P | sed 's/^/        /')
        printf "    ${i}: 
        volumeMounts:
${vol}\n"
        count=$((count+1))
    done
    # list containers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.containers[*].name}')
    printf "\n  containers: \n"
    for i in $imgname; do
        vol=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.containers[${count}].volumeMounts}" | yq -P | sed 's/^/        /')
        printf "    ${i}: 
        volumeMounts:
${vol}\n"
        count=$((count+1))
    echo ""
    done
    # list shared volumes for pod
    vol=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.volumes}" | yq -P | sed 's/^/        /')
    printf "  shared volumes for ${1} pod:
      volumes:
${vol}\n"
    echo ""
}

get_resources() {
    # list initContainers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.initContainers[*].name}')
    printf "\n  initContainers: \n"
    for i in $imgname; do
        vol=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.initContainers[${count}].resources}" | yq -P | sed 's/^/        /')
        printf "    ${i}: 
        resources:
${vol}\n"
        count=$((count+1))
    done
    # list containers for pod
    count=0
    imgname=$(kubectl get pod $namespace $1 -o jsonpath='{.spec.containers[*].name}')
    printf "\n  containers: \n"
    for i in $imgname; do
        vol=$(kubectl get pod $namespace $1 -o jsonpath="{.spec.containers[${count}].resources}" | yq -P | sed 's/^/        /')
        printf "    ${i}: 
        resources:
${vol}\n"
        count=$((count+1))
    echo ""
    done
}

get_pods() {
    echo ""
    if [[ "$(kubectl get pod $namespace $1 -o jsonpath='{.kind}')" == "List" ]]; then
        # handle list of pods
        podnames=$(kubectl get pod $namespace -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name --no-headers)
        echo "$podnames" | while IFS= read -r i; do
            namespace="-n $(echo $i | awk '{print $1}')"
            pod=$(echo $i | awk '{print $2}')
            echo -e "pod: ${color}$pod (${namespace}):${nc}"
            if [[ $podaction == "images" ]]; then
                get_images $pod
            elif [[ $podaction == "volumes" ]]; then
                get_volumes $pod
            elif [[ $podaction == "resources" ]]; then
                get_resources $pod
            fi
        done
    else
        # handle individual pod
        pod=$(kubectl get pod $namespace $1 -o custom-columns=NAME:.metadata.name --no-headers)
        echo "pod:" $pod
        if [[ $podaction == "images" ]]; then
            get_images $pod
        elif [[ $podaction == "volumes" ]]; then
            get_volumes $pod
        elif [[ $podaction == "resources" ]]; then
            get_resources $pod
        fi
    fi
}

kubeconfig() {

    csr() {
        csrkey=$(mktemp)
        csrreq=$(mktemp)
        openssl req --new --newkey rsa:4096 --nodes --keyout ${csrkey} --out ${csrreq} --subj "/CN=${newuser},/O=readonlyusers" 2>/dev/null
        csr=$(cat ${csrreq} | base64 | tr -d '\n')

        cat << EOF | kubectl apply -f -
---
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: ${newuser}
spec:
    signerName: kubernetes.io/kube-apiserver-client
    request: $csr
    usages:
        - client auth
EOF
        kubectl certificate approve ${newuser}
        usercert=$(kubectl get csr ${newuser} -o jsonpath='{.status.certificate}')
    }

    # function to create new service account
    serviceaccount() {
        cat <<EOF | kubectl apply -f - 2>&1 >/dev/null &
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${newuser}
  namespace: ${namespace}
EOF
    }

    # function for clusterrole
    clusterrole() {
        cat <<EOF | kubectl apply -f - 2>&1 >/dev/null &
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${newuser}-role
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
EOF
    }

    # function for clusterrolebinding
    clusterrolebinding() {
        kubectl create clusterrolebinding ${newuser}-binding --clusterrole=${newuser}-role --user=${newuser}
#         cat <<EOF | kubectl apply -f - 2>&1 >/dev/null &
# ---
# apiVersion: rbac.authorization.k8s.io/v1
# kind: ClusterRoleBinding
# metadata:
#   name: ${newuser}-binding
# roleRef:
#   apiGroup: rbac.authorization.k8s.io
#   kind: ClusterRole
#   name: ${newuser}-role
# subjects:
#   - apiGroup: rbac.authorization.k8s.io
#     kind: User
#     name: ${newuser}
# EOF
    }

    # secret for sa
    sasecret() {
        cat <<EOF | kubectl apply -f - 2>&1 >/dev/null &
---
apiVersion: v1
kind: Secret
metadata:
  name: ${newuser}
  namespace: ${namespace}
  annotations:
    kubernetes.io/service-account.name: ${newuser}
type: kubernetes.io/service-account-token
EOF
    }

    # kubeconfig
    construct_kubeconfig() {
        echo "---
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CATOKEN}
    server: ${SERVER}
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: ${newuser}
  name: ${newuser}
current-context: ${newuser}
users:
- name: ${newuser}
  user:
    client-certificate-data: ${usercert}
    client-key-data: $(cat ${csrkey} | base64 | tr -d '\n')
"
    }

    if [[ "${2}" == "readonly" || "${2}" == "ro" ]]; then
        if [[ "$3" ]]; then
            newuser="readonly-${3}"
        else
            newuser="readonly-user"
        fi
        # get rid of -n or --namespace for namespace var
        namespace=$(echo ${namespace} | sed 's/\-n//g')
        namespace=$(echo ${namespace} | sed 's/\--namespace//g')

        # serviceaccount
        csr
        clusterrole
        clusterrolebinding
        # sasecret

        # token takes a sec
        sleep 1

        # TOKEN=$(kubectl get secret ${newuser} -n ${namespace} -o jsonpath="{.data.token}" | base64 --decode)
        CATOKEN=$(kubectl config view --raw -o jsonpath="{.clusters[0].cluster.certificate-authority-data}")
        # CATOKEN=$(kubectl get secret ${newuser} -n ${namespace} -o jsonpath="{.data.ca\.crt}")
        SERVER=$(kubectl config view --raw -o jsonpath="{.clusters[0].cluster.server}")

        construct_kubeconfig
    fi

}

namespace() {
    # determine namespace separately so it can go anywhere in user input like kubectl
    while [[ $# -gt 0 ]]
    do
        case "$1" in
        -n|--namespace)
            export namespace="--namespace ${2}"
            ;;
        -A|--all-namespaces)
            export namespace="--all-namespaces"
            ;;
        esac
        shift
    done
}

# CLI Flags
flags()
{
    while [[ $# -gt 0 ]]
    do
        case "$1" in
        -n|--namespace)
            # just to not trip help since namespace is handled separately
            ;;
        -A|--all-namespaces)
            # just to not trip help since namespace is handled separately
            ;;
        get)
            if [[ "$*" == *"-o yaml"* || "$*" == *"-oyaml"* ]]; then
                # TODO error handling for yq dependency
                decode "$@"
                neat "$@"
            else
                kubectl "$@"
            fi
            exit 0
            ;;
        healthcheck|health)
            healthcheck
            ;;
        image|images|img|imgs)
            podaction="images"
            if [[ "${2}" == "-n" || "${2}" == "--namespace" ]]; then
                get_pods $4
            else
                get_pods $2
            fi
            exit 0
            ;;
        resource|resources|res)
            podaction="resources"
            if [[ "${2}" == "-n" || "${2}" == "--namespace" ]]; then
                get_pods $4
            else
                get_pods $2
            fi
            exit 0
            ;;
        volume|volumes|vol|vols)
            podaction="volumes"
            if [[ "${2}" == "-n" || "${2}" == "--namespace" ]]; then
                get_pods $4
            else
                get_pods $2
            fi
            exit 0
            ;;
        kubeconfig)
            if [[ "$@" == *"-n"* || "$@" == *"--namespace"* ]]; then
                echo "Do not declare namespaces for 'k kubeconfig'"
                echo "For namespaced RBAC, run the 'k kubeconfig newuser namespace <namespace> <username>'"
                exit 0
            fi
            kubeconfig "$@"
            exit 0
            ;;
        -h|--help|h|help)
            cli_help
            ;;
        *)
            # catch all for other kubectl functions not mutated by kelper
            kubectl "$@"
            exit 0
            ;;
        esac
        shift
    done

    # TODO add preflight check for k8s access

    # TODO add function to verify kubectl and api versions

    # if no option chosen
    cli_help
}

namespace "$@"
flags "$@"
