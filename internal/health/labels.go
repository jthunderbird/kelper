package health

import "k8s.io/apimachinery/pkg/labels"

// labelsEverything is the selector used for every lister read; the snapshot is
// scoped by namespace rather than by labels.
func labelsEverything() labels.Selector { return labels.Everything() }
