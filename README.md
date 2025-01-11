# Kuberetes Helper (kelper)
Kelper wraps kubectl to add in ease-of-use features

## Examples

```bash
alias k=kelper

### resources focused
k get secret -n ns mysecret -o yaml # output to decoded secret values, error handling for lack of yq - maybe switch to disable
k get po -n ns mypod -o yaml # auto-neat - maybe a switch to disable neat
k healthcheck # runs health check on all applications in cluster - conditional for istio, future for nginx and traefik, only https
k healthcheck watch # watches health - assumption being watching a deployment come online
k healthcheck -n ns # health in specific namespace
k app -n ns # lists all possible applications - ds,deploy,sts,rs,job,cronjob,raw-pod,replicacontrol(warning if this is there)
k images -n ns (mypod) # lists all images and maps to pod>initContainer>Container

### user/context focused
k kubeconfig readonly > ro-config # interact with cluster to make readonly config - default to STDOUT
k kubeconfig newuser johndoe readonly/admin > johnadmin.yaml # same as above but creating at least trackable users
k get users/contexts # prints out all available contexts and users and shows who you are and which cluster you are on

### ux focused
k autocomplete # auto setup autocomplete for kelper and kubectl by checking shell and configuring for suported shells
```
