# kubectl helper (kelper)

bash utility to make kubectl more better

## Installing

Just a bash script, put it in your $PATH and profit:

```bash
git clone https://github.com/jthunderbird/kelper.git
chmod +x kelper/kelper
cp kelper/kelper /usr/local/bin/kelper
alias k=kelper # or for pros # ln -s /usr/local/bin/kelper /usr/local/bin/k
k help
```

## Getting Started

```bash
alias k=kelper

### resources focused
k get secret -n ns mysecret -o yaml # output to decoded secret values
k get po -n ns mypod -o yaml # auto-neat - yq removes unnecessary fields
k healthcheck # runs health check on all applications in cluster
k healthcheck -n ns # health in specific namespace
k healthcheck watch # watches health
k images -n ns (mypod) # lists all images and maps to pod>initContainer>Container
k resources # clearly show limits and requests per pod or list
k volumes # clearly show volume and volume mounts per pod or list

### user/context focused EXPERIMENTAL
k kubeconfig readonly > ro-config # interact with cluster to make readonly config - default to STDOUT

k kubeconfig newuser johndoe readonly/admin/namespace > johnadmin.yaml # same as above but creating at least trackable users
k kubeconfig context newuser johndoe readonly/admin/namespace # same as above but adds context to existing kubeconfig instead of creating a new one
k users/contexts # prints out all available contexts and users and shows who you are and which cluster you are on

### ux focused FUTURE
k autocomplete # auto setup autocomplete for kelper and kubectl by checking shell and configuring for suported shells
```

## in action

### Secrets

Auto decodes and prints out the key and decoded value:

```bash
user3@workstation:~/git/test-cluster$ k get secret -n kiali grafana-auth -o yaml
password: prom-operator
```

### -o yaml

Any `-o yaml` that is not a secret, `kelper` cleans up the output by removing the auto mutations Kubernetes throws in there. The output is usually perfect for copying to a yaml file as there are no cumbersome fields like UUID, timestamps, status, etc.

```bash
user3@workstation:~/git/test-cluster$ k get po -n flux-system helm-controller-678f5576df-g7scx -o yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    prometheus.io/port: "8080"
    prometheus.io/scrape: "true"
  labels:
    app: helm-controller
    app.kubernetes.io/component: helm-controller
    app.kubernetes.io/instance: flux-system
    app.kubernetes.io/part-of: flux
    app.kubernetes.io/version: v2.7.0
    pod-template-hash: 678f5576df
  name: helm-controller-678f5576df-g7scx
  namespace: flux-system
spec:
  containers:
    - args:
        - --events-addr=http://notification-controller.flux-system.svc.cluster.local./
        - --watch-all-namespaces=true
        - --log-level=info
        - --log-encoding=json
        - --enable-leader-election
      env:
        - name: RUNTIME_NAMESPACE
          valueFrom:
            fieldRef:
              apiVersion: v1
              fieldPath: metadata.namespace
        - name: GOMEMLIMIT
          valueFrom:
            resourceFieldRef:
              containerName: manager
              divisor: "0"
              resource: limits.memory
      image: docker.io/fluxcd/helm-controller:v1.4.0
      imagePullPolicy: IfNotPresent
      livenessProbe:
        failureThreshold: 3
        httpGet:
          path: /healthz
          port: healthz
          scheme: HTTP
        periodSeconds: 10
        successThreshold: 1
        timeoutSeconds: 1
      name: manager
      ports:
        - containerPort: 8080
          name: http-prom
          protocol: TCP
        - containerPort: 9440
          name: healthz
          protocol: TCP
      readinessProbe:
        failureThreshold: 3
        httpGet:
          path: /readyz
          port: healthz
          scheme: HTTP
        periodSeconds: 10
        successThreshold: 1
        timeoutSeconds: 1
      resources:
        limits:
          cpu: "1"
          memory: 1Gi
        requests:
          cpu: 100m
          memory: 64Mi
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop:
            - ALL
        readOnlyRootFilesystem: true
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      terminationMessagePath: /dev/termination-log
      terminationMessagePolicy: File
      volumeMounts:
        - mountPath: /tmp
          name: temp
        - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
          name: kube-api-access-msmzp
          readOnly: true
  dnsPolicy: ClusterFirst
  enableServiceLinks: true
  nodeName: flux-control-plane
  nodeSelector:
    kubernetes.io/os: linux
  preemptionPolicy: PreemptLowerPriority
  priority: 2000000000
  priorityClassName: system-cluster-critical
  restartPolicy: Always
  schedulerName: default-scheduler
  securityContext:
    fsGroup: 1337
  serviceAccount: helm-controller
  serviceAccountName: helm-controller
  terminationGracePeriodSeconds: 600
  tolerations:
    - effect: NoExecute
      key: node.kubernetes.io/not-ready
      operator: Exists
      tolerationSeconds: 300
    - effect: NoExecute
      key: node.kubernetes.io/unreachable
      operator: Exists
      tolerationSeconds: 300
  volumes:
    - emptyDir: {}
      name: temp
    - name: kube-api-access-msmzp
      projected:
        defaultMode: 420
        sources:
          - serviceAccountToken:
              expirationSeconds: 3607
              path: token
          - configMap:
              items:
                - key: ca.crt
                  path: ca.crt
              name: kube-root-ca.crt
          - downwardAPI:
              items:
                - fieldRef:
                    apiVersion: v1
                    fieldPath: metadata.namespace
                  path: namespace
```

### Healthcheck

This checks for any services in the cluster not running properly. Unlike a basic check just to see if pods are ready or if the number ready matches the number expected, this checks for deployments, statefulsets, daemonsets, and jobs to ensure they are healthy to also catch broken pods that have not been scheduled (kyverno can stop them, as can some helm hooks and other things).

In the example below, we can see that the `istio-operator` namespace has what looks to be a fully functional deployment when running `k get pods -n istio-operator` but our healthcheck found that a job had failed.

```bash
user3@workstation:~/git/test-cluster$ k get po -n istio-operator
NAME                              READY   STATUS    RESTARTS   AGE
istio-operator-7765959ff8-fpsnz   1/1     Running   0          20h
user3@workstation:~/git/test-cluster$ k health -A
Unhealthy pods:
Unhealthy applications (deploy,sts,rs,ds,job,cronjob):
istio-operator   job.batch/istiod-hook   Failed   0/1   19h   19h
```

### Images

This prints out the pod name, names of any initContainers and Containers, and the images those containers are using.

```bash
user3@workstation:~/git/test-cluster$ k images -n kyverno

pod: kyverno-admission-controller-5d8986c8b6-2g7gr (-n kyverno):

  initContainers: 
    kyverno-pre: registry1.dso.mil/ironbank/opensource/kyverno/kyvernopre:v1.13.4

  containers: 
    kyverno: registry1.dso.mil/ironbank/opensource/kyverno:v1.13.4

pod: kyverno-background-controller-75c6b48976-t4hvx (-n kyverno):

  initContainers: 

  containers: 
    controller: registry1.dso.mil/ironbank/opensource/kyverno/kyverno/background-controller:v1.13.4

pod: kyverno-cleanup-controller-7b868db7cb-q6j89 (-n kyverno):

  initContainers: 

  containers: 
    controller: registry1.dso.mil/ironbank/opensource/kyverno/kyverno/cleanup-controller:v1.13.4

pod: kyverno-reports-controller-5f9b7f6f7f-cn75t (-n kyverno):

  initContainers: 

  containers: 
    controller: registry1.dso.mil/ironbank/opensource/kyverno/kyverno/reports-controller:v1.13.4
```

### Resources

This prints out the configured `requests:` and `limits:` resources for each container in a pod.

```bash
user3@workstation:~/git/test-cluster$ k resources -n kyverno

pod: kyverno-admission-controller-5d8986c8b6-2g7gr (-n kyverno):

  initContainers: 
    kyverno-pre: 
        resources:
        limits:
          cpu: "1"
          memory: 1Gi
        requests:
          cpu: 10m
          memory: 64Mi

  containers: 
    kyverno: 
        resources:
        limits:
          cpu: 500m
          memory: 512Mi
        requests:
          cpu: 100m
          memory: 128Mi

pod: kyverno-background-controller-75c6b48976-t4hvx (-n kyverno):

  initContainers: 

  containers: 
    controller: 
        resources:
        limits:
          memory: 128Mi
        requests:
          cpu: 100m
          memory: 64Mi

pod: kyverno-cleanup-controller-7b868db7cb-q6j89 (-n kyverno):

  initContainers: 

  containers: 
    controller: 
        resources:
        limits:
          memory: 128Mi
        requests:
          cpu: 100m
          memory: 64Mi

pod: kyverno-reports-controller-5f9b7f6f7f-cn75t (-n kyverno):

  initContainers: 

  containers: 
    controller: 
        resources:
        limits:
          memory: 128Mi
        requests:
          cpu: 100m
          memory: 64Mi
```

### Volumes

Prints out the configured `volumeMounts` per container in a pod as well as the `volumes` configuration per pod.

```bash
user3@workstation:~/git/test-cluster$ k volumes -n istio-operator

pod: istio-operator-7765959ff8-fpsnz (-n istio-operator):

  initContainers: 

  containers: 
    istio-operator: 
        volumeMounts:
        - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
          name: kube-api-access-8nh58
          readOnly: true

  shared volumes for istio-operator-7765959ff8-fpsnz pod:
      volumes:
        - name: kube-api-access-8nh58
          projected:
            defaultMode: 420
            sources:
              - serviceAccountToken:
                  expirationSeconds: 3607
                  path: token
              - configMap:
                  items:
                    - key: ca.crt
                      path: ca.crt
                  name: kube-root-ca.crt
              - downwardAPI:
                  items:
                    - fieldRef:
                        apiVersion: v1
                        fieldPath: metadata.namespace
                      path: namespace
```
