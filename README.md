# kubectl helper (kelper)

janky bash utility to make kubectl more better

## Installing

Just a janky bash script, put it in your $PATH and profit:

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
k volumes # clearly show volume and volume mounts per pod or list

### user/context focused
k kubeconfig readonly > ro-config # interact with cluster to make readonly config - default to STDOUT
k kubeconfig newuser johndoe readonly/admin/namespace > johnadmin.yaml # same as above but creating at least trackable users
k kubeconfig context newuser johndoe readonly/admin/namespace # same as above but adds context to existing kubeconfig instead of creating a new one
k users/contexts # prints out all available contexts and users and shows who you are and which cluster you are on

### ux focused
k autocomplete # auto setup autocomplete for kelper and kubectl by checking shell and configuring for suported shells
```
