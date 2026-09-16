# 🧪 Testing

## 📜 Table of Contents

1. [Running the tests](#-running-the-tests)
2. [What is covered](#-what-is-covered)
3. [Mocks](#-mocks)
4. [Writing a test](#-writing-a-test)
5. [Manual verification against a real cluster](#-manual-verification-against-a-real-cluster)
6. [CSI sanity](#-csi-sanity)
7. [What CI runs](#-what-ci-runs)

## ▶ Running the tests

```sh
mise run test
```

That is `go test ./... -cover -coverprofile=coverage.out -outputdir=. -coverpkg=./...`. Note `-coverpkg=./...`, which measures coverage across every package rather than only the package under test, so the number reflects how much of the driver the whole suite exercises.

```sh
# Coverage as HTML
mise run cover

# One package
go test ./internal/driver/...

# One test, verbose
go test ./internal/driver/ -run TestCreateVolume -v

# With the race detector
go test -race ./...
```

The whole suite is hermetic: no network, no cluster, no Linode token. If a test needs any of those, it is in the wrong place.

## 🎯 What is covered

| Test file | Subject |
| --- | --- |
| `internal/driver/controllerserver_test.go` | Every controller RPC, including error paths |
| `internal/driver/controller_helpers_test.go` | Parameter parsing, handle formats, label normalization, idempotency comparisons, error mapping |
| `internal/driver/nodeserver_test.go` | Node RPC validation, locking, stats |
| `internal/driver/nodeserver_helpers_test.go` | Mount option assembly, the mTLS decision, the optional-mTLS fallback |
| `internal/driver/metadata_test.go` | Provider ID parsing, region resolution, both VPC discovery paths, the multi-region rejection |
| `internal/driver/identityserver_test.go` | Plugin info, capabilities, probe readiness |
| `internal/driver/driver_test.go` | Setup, role validation, capability advertisement |
| `pkg/linode-client/client_test.go` | Client construction and configuration |
| `pkg/mount-manager/safe_mounter_test.go` | The mounter wrapper |

The parts worth having tests for, and which do have them, are the ones that are painful to verify by hand: the composite handle formats, the 63-byte label truncation with its trailing-hyphen strip, the HTTP-to-gRPC error mapping table, and the two different VPC discovery paths for legacy and modern interface generations.

## 🎭 Mocks

Three interfaces are mocked with `gomock`, generated into `mocks/`:

| Mock | Generated from |
| --- | --- |
| `mocks/mock_linodeclient.go` | `pkg/linode-client/client.go` |
| `mocks/mock_safe-mounter.go` | `pkg/mount-manager/safe_mounter.go` |
| `mocks/mock_filesystem.go` | `pkg/filesystem/filesystem.go` |

```sh
mise run gen-mock
```

Regenerate whenever you change one of those interfaces, and commit the result. `mise run ci` runs `gen-mock` before `vet`, so stale mocks surface as a compile error in CI rather than as a confusing test failure. Never edit a file in `mocks/` by hand.

`mocks/mock_metadata.go` also exists and is checked in but is not produced by `gen-mock`; leave it alone unless you are changing the metadata interfaces deliberately.

The mock client is what makes the controller tests possible: they assert on the exact sequence of Linode API calls, which is the only way to pin down behavior like "publish makes no API write when the Linode is already in the ACL".

## ✍ Writing a test

The existing tests are table-driven with `gomock` expectations. The shape to follow:

```go
func TestSomething(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		setup   func(*mocks.MockLinodeClient)
		wantErr codes.Code
	}{
		{
			name: "success",
			setup: func(c *mocks.MockLinodeClient) {
				c.EXPECT().GetNFSSpace(gomock.Any(), 42).Return(&linodego.NFSSpace{ID: 42}, nil)
			},
		},
		{
			name: "space not found",
			setup: func(c *mocks.MockLinodeClient) {
				c.EXPECT().GetNFSSpace(gomock.Any(), 42).Return(nil, &linodego.Error{Code: 404})
			},
			wantErr: codes.NotFound,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// ...
		})
	}
}
```

Two conventions that matter here:

- **Assert on the gRPC code, not the message string.** The codes are the contract with the sidecars; the messages are not.
- **Cover the idempotent path.** Every operation that can be retried has a "second call finds the existing resource" path, and that path is where the real bugs live. A new RPC without such a test is incomplete.

Run `mise run lint` before pushing. `golangci-lint run --fix` will reformat as it goes, so run it before you finalize a diff rather than after.

## 🔬 Manual verification against a real cluster

The unit tests cannot tell you whether a mount works. For that, use the [development cluster](./development-setup.md#-creating-a-development-cluster) and walk the full lifecycle.

```sh
export KUBECONFIG="$(pwd)/nfs-csi-driver-dev-kubeconfig"
```

**1. The driver registered.**

```sh
kubectl -n kube-system get pods -l role=csi-linode
kubectl get csidrivers linodenfs.csi.linode.com
kubectl get csinodes -o custom-columns=NODE:.metadata.name,DRIVERS:.spec.drivers[*].name
```

Every node must list the driver. A node that does not will never mount anything.

**2. Provisioning.**

```sh
kubectl apply -f - <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: linode-nfs-test
provisioner: linodenfs.csi.linode.com
parameters:
  linodenfs.csi.linode.com/space-id: "42"
reclaimPolicy: Delete
allowVolumeExpansion: false
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes: [ReadWriteMany]
  storageClassName: linode-nfs-test
  resources:
    requests:
      storage: 10Gi
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/test-pvc --timeout=10m
```

Ten minutes, not one: provisioning includes up to two 5-minute backend waits (the space access policy and the filesystem).

**3. Mounting, and the multi-node case that matters most.**

```sh
kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-writers
spec:
  replicas: 3
  selector:
    matchLabels: {app: test-writers}
  template:
    metadata:
      labels: {app: test-writers}
    spec:
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels: {app: test-writers}
              topologyKey: kubernetes.io/hostname
      containers:
        - name: writer
          image: busybox
          command: [/bin/sh, -c, 'echo "$(hostname) $(date)" >> /data/shared.log; sleep 86400']
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: test-pvc}
EOF

kubectl rollout status deploy/test-writers --timeout=5m
kubectl exec deploy/test-writers -- cat /data/shared.log
```

The anti-affinity forces one replica per node, so `shared.log` containing three distinct hostnames proves that `ReadWriteMany` across nodes actually works. That is the single most valuable manual check, because it is exactly what unit tests cannot cover.

**4. Volume stats.**

```sh
kubectl exec deploy/test-writers -- df -h /data
```

**5. Snapshot and restore**, if you enabled the snapshotter. See [Snapshots](./snapshots.md).

**6. Cleanup, in order.**

```sh
kubectl delete deploy test-writers
kubectl delete pvc test-pvc
kubectl delete sc linode-nfs-test
```

Then confirm the backend filesystem is actually gone, since a leak here is invisible from Kubernetes:

```sh
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces/42/filesystems
```

Things worth deliberately breaking while you are in here, because each one exercises a distinct code path that is easy to regress:

| Scenario | Expected |
| --- | --- |
| A `StorageClass` with neither space parameter | PVC stays `Pending` with `InvalidArgument` in the events |
| A `space-id` that does not exist | `NotFound` |
| `volumeMode: Block` | `InvalidArgument: only mount volume capabilities are supported` |
| A `dataSource` of `kind: PersistentVolumeClaim` | `InvalidArgument: unsupported volume content source` |
| Deleting the PVC while a pod still uses it | The PVC waits on `pvc-protection` until the pod is gone |
| Restarting the node DaemonSet with a pod mounted | The pod keeps working; the mount lives in the host namespace |
| Two pods on the same node sharing the PVC | Both start without serializing; publish locks the path, not the volume |

## 🧷 CSI sanity

The repository does not currently run [csi-sanity](https://github.com/kubernetes-csi/csi-test) in CI. If you run it manually, expect and ignore failures for the capabilities the driver does not advertise: `ListVolumes`, `GetCapacity`, expansion, and cloning. `ListSnapshots` is a special case, since the RPC works but the capability is not advertised, so sanity may skip tests that would in fact pass.

The driver's socket is reachable inside the plugin container:

```sh
# controller
kubectl -n kube-system exec deploy/csi-linode-nfs-controller -c plugin -- \
  ls -l /var/lib/csi/sockets/pluginproxy/csi.sock
```

## 🤖 What CI runs

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci.yml` | Push to `main`, every PR | `mise run ci`: `fmt`, `gen-mock`, `vet`, `lint`, `test`, `build` |
| `helm.yml` | PRs touching `charts/`, `deploy/kubernetes/`, the hack scripts, `justfile`, or `mise.toml` | `helm lint`, then `verify-kustomize` |
| `actionlint.yml` | Workflow changes | Lints the workflow files |
| `image-build-push.yml` | See the workflow | Builds and publishes images |
| `release.yml` | Tags | Builds the image, publishes the chart via chart-releaser, attaches release artifacts |

`mise run ci` locally is the same command CI runs, so a green local run means a green pipeline.

Every workflow runs `step-security/harden-runner`, but the egress policy is not uniform. `ci.yml`, `helm.yml`, and `automerge.yml` use `egress-policy: block` with an explicit `allowed-endpoints` list; the rest use `audit`, which only records outbound connections. So in those three, adding a dependency that fetches from a new host fails the job in a way that looks unrelated to your change. If a build starts failing on a network error, check that workflow's `allowed-endpoints` before you suspect your code.

Note that `ci.yml` does **not** run `verify-kustomize`; that lives in `helm.yml` and only fires on the paths listed above. A chart change bundled into a PR that also touches Go code will still trigger it, since the path filter matches on any changed file.

## 📚 Related pages

- [Development Setup](./development-setup.md)
- [Contributing](../.github/CONTRIBUTING.md)
- [CSI RPC reference](./csi-rpc-reference.md)
