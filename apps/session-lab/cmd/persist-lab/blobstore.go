package main

// Where the blobs go. The chart at config/minio deploys exactly one Service and
// one Secret, so the runner reads both from the cluster it already talks to: the
// endpoint from the Service (ClusterIP, or the node's InternalIP plus the node
// port when the Service is exposed) and the credentials from the Secret Helm
// created. No flag points the arm anywhere else, and there is nowhere to fall
// back to if it is missing — the run stops and says which object it could not
// find.

import (
	"context"
	"fmt"
	"log"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/persist"
)

const (
	// minioService and minioSecret are the names config/minio creates.
	minioService = "minio"
	minioPort    = 9000
	minioSecret  = "minio-credentials"
	// minioBucket is where every run writes. The writer creates it on its first
	// PUT, and runs are separated by the key prefix instead of by bucket.
	minioBucket = "session-lab"
)

// openBlobStore returns a writer over the cluster's MinIO. The plan rides along so
// each capture is counted against the class of state its session carried.
func openBlobStore(ctx context.Context, adapter *kube.Kube, namespace, runID string,
	rate float64, budget int64, plan persist.Plan, log *log.Logger) (persist.Store, error) {
	endpoint, err := adapter.ServiceEndpoint(ctx, namespace, minioService, minioPort)
	if err != nil {
		return nil, fmt.Errorf("persist: %w", err)
	}
	credentials, err := adapter.SecretData(ctx, namespace, minioSecret)
	if err != nil {
		return nil, fmt.Errorf("persist: %w", err)
	}
	access, secret := credentials["accessKey"], credentials["secretKey"]
	if len(access) == 0 || len(secret) == 0 {
		return nil, fmt.Errorf("persist: secret %s/%s carries no accessKey and secretKey",
			namespace, minioSecret)
	}
	writer, err := persist.NewS3(ctx, persist.S3Options{
		Endpoint:  endpoint,
		AccessKey: string(access),
		SecretKey: string(secret),
		Bucket:    minioBucket,
		Prefix:    "runs/" + runID,
		Rate:      rate,
		Budget:    budget,
		Dataset:   func(sessionID int64) string { return plan.For(sessionID).Label },
		Log:       log,
	})
	if err != nil {
		return nil, fmt.Errorf("persist: %w", err)
	}
	return writer, nil
}
