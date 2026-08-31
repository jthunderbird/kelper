package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"
)

// summaryConcurrency bounds parallel kubelet queries so a large cluster does
// not open hundreds of simultaneous proxy connections.
const summaryConcurrency = 8

// statsSummary is the subset of the kubelet Summary API that carries volume
// usage. PVC utilization is not exposed by metrics-server, so this endpoint is
// the only in-cluster source for it.
type statsSummary struct {
	Node struct {
		NodeName string `json:"nodeName"`
		Fs       *struct {
			CapacityBytes *uint64 `json:"capacityBytes"`
			UsedBytes     *uint64 `json:"usedBytes"`
			InodesFree    *uint64 `json:"inodesFree"`
			InodesUsed    *uint64 `json:"inodesUsed"`
		} `json:"fs"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Volume []struct {
			Name          string  `json:"name"`
			CapacityBytes *uint64 `json:"capacityBytes"`
			UsedBytes     *uint64 `json:"usedBytes"`
			InodesFree    *uint64 `json:"inodesFree"`
			InodesUsed    *uint64 `json:"inodesUsed"`
			PVCRef        *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

// VolumeUsage is kubelet-reported usage for one PersistentVolumeClaim.
type VolumeUsage struct {
	Namespace     string
	PVC           string
	Node          string
	UsedBytes     uint64
	CapacityBytes uint64
	InodesUsed    uint64
	InodesFree    uint64
}

// NodeFsUsage is a node's own filesystem usage.
type NodeFsUsage struct {
	Node          string
	UsedBytes     uint64
	CapacityBytes uint64
	InodesUsed    uint64
	InodesFree    uint64
}

// FetchVolumeStats queries the kubelet summary endpoint on each node through
// the API server proxy and returns PVC usage keyed "namespace/pvc" plus each
// node's filesystem usage. Unreachable nodes are skipped: one broken kubelet
// must not fail the run.
func FetchVolumeStats(ctx context.Context, kube kubernetes.Interface, nodes []string) (map[string]VolumeUsage, map[string]NodeFsUsage) {
	vols := map[string]VolumeUsage{}
	fs := map[string]NodeFsUsage{}
	if kube == nil || len(nodes) == 0 {
		return vols, fs
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, summaryConcurrency)
	)
	for _, node := range nodes {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			raw, err := kube.CoreV1().RESTClient().Get().
				Resource("nodes").Name(node).SubResource("proxy").
				Suffix("stats", "summary").
				DoRaw(ctx)
			if err != nil {
				return
			}
			var sum statsSummary
			if err := json.Unmarshal(raw, &sum); err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			if f := sum.Node.Fs; f != nil {
				fs[node] = NodeFsUsage{
					Node:          node,
					UsedBytes:     deref(f.UsedBytes),
					CapacityBytes: deref(f.CapacityBytes),
					InodesUsed:    deref(f.InodesUsed),
					InodesFree:    deref(f.InodesFree),
				}
			}
			for _, p := range sum.Pods {
				for _, v := range p.Volume {
					if v.PVCRef == nil {
						continue
					}
					key := fmt.Sprintf("%s/%s", v.PVCRef.Namespace, v.PVCRef.Name)
					vols[key] = VolumeUsage{
						Namespace:     v.PVCRef.Namespace,
						PVC:           v.PVCRef.Name,
						Node:          node,
						UsedBytes:     deref(v.UsedBytes),
						CapacityBytes: deref(v.CapacityBytes),
						InodesUsed:    deref(v.InodesUsed),
						InodesFree:    deref(v.InodesFree),
					}
				}
			}
		}(node)
	}
	wg.Wait()
	return vols, fs
}

func deref(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
