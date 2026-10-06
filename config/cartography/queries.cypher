// Cartography Cypher query pack — queries verified against the local cluster.
//
// Connect (Neo4j Browser):
//   URL:        neo4j://neo4j.localhost:7687   (or neo4j://192.168.0.242:7687)
//   Database:   neo4j
//   Auth:       none (blank user/password)
//
// Or from a shell (inside the cluster):
//   kubectl -n cartography exec deploy/cartography-neo4j -- \
//     cypher-shell -a bolt://localhost:7687 --non-interactive "<query>"
//
// Data is (re)built by the Cartography CronJob every 6h; trigger one early with:
//   kubectl -n cartography create job --from=cronjob/cartography cartography-manual
//
// Labels in this graph: Kubernetes{Cluster,Namespace,Node,Pod,Container,Service,
// Deployment,StatefulSet,DaemonSet,ReplicaSet,Job,CronJob,ServiceAccount,Role,
// RoleBinding,ClusterRole,ClusterRoleBinding,NetworkPolicy,Ingress,Secret,
// PersistentVolume,PersistentVolumeClaim,StorageClass}. Relationship types:
// CONTAINS, RUNS_ON, USES_SERVICE_ACCOUNT, RUNS_AS, WORKLOAD_PARENT, OWNED_BY,
// TARGETS, APPLIES_TO, SUBJECT, ROLE_REF, RESOURCE, REFERENCES, BOUND_TO,
// USES_STORAGE_CLASS.


// ===========================================================================
// 1. Inventory — what is in the graph
// ===========================================================================

// Node count per label (the "what do I have" query).
MATCH (n) UNWIND labels(n) AS label
RETURN label, count(*) AS nodes
ORDER BY nodes DESC;

// Total node count.
MATCH (n) RETURN count(n) AS total_nodes;

// Cluster(s) and node(s).
MATCH (n:KubernetesNode) RETURN n.name AS node, n.architecture AS architecture;
MATCH (c:KubernetesCluster) RETURN c.name AS cluster;


// ===========================================================================
// 2. Pods, namespaces, workloads
// ===========================================================================

// Every pod: namespace, name, node, phase.
MATCH (p:KubernetesPod)
RETURN p.namespace AS namespace, p.name AS pod, p.node AS node, p.status_phase AS phase
ORDER BY namespace, pod;

// Pods per namespace.
MATCH (p:KubernetesPod)
RETURN p.namespace AS namespace, count(*) AS pods
ORDER BY pods DESC;

// Deployment -> its pods.
MATCH (d:KubernetesDeployment)<-[:WORKLOAD_PARENT]-(p:KubernetesPod)
RETURN d.namespace AS namespace, d.name AS deployment, collect(p.name) AS pods
ORDER BY namespace, deployment;

// Pods per node (scheduling view).
MATCH (p:KubernetesPod)-[:RUNS_ON]->(n:KubernetesNode)
RETURN n.name AS node, collect(p.name) AS pods;

// Pod -> owning ReplicaSet -> Deployment (the controller chain).
MATCH (p:KubernetesPod)-[:OWNED_BY]->(rs:KubernetesReplicaSet)
OPTIONAL MATCH (rs)<-[:WORKLOAD_PARENT]-(d:KubernetesDeployment)
RETURN p.name AS pod, rs.name AS replicaset, d.name AS deployment;


// ===========================================================================
// 3. Networking & exposure — what is reachable from outside
// ===========================================================================

// Services and the pods they target.
MATCH (s:KubernetesService)-[:TARGETS]->(p:KubernetesPod)
RETURN s.namespace AS namespace, s.name AS service, collect(DISTINCT p.name) AS pods
ORDER BY namespace, service;

// Full external path: Ingress -> Service -> Pod.
MATCH (i:KubernetesIngress)-[:TARGETS]->(s:KubernetesService)-[:TARGETS]->(p:KubernetesPod)
RETURN i.namespace AS namespace, i.name AS ingress, s.name AS service,
       collect(DISTINCT p.name) AS pods;

// Network policies and the pods they apply to (segmentation view).
MATCH (np:KubernetesNetworkPolicy)-[:APPLIES_TO]->(p:KubernetesPod)
RETURN np.namespace AS namespace, np.name AS policy, collect(p.name) AS pods
ORDER BY namespace, policy;

// Pods NOT covered by any NetworkPolicy (candidate exposure).
MATCH (p:KubernetesPod)
WHERE NOT (:KubernetesNetworkPolicy)-[:APPLIES_TO]->(p)
RETURN p.namespace AS namespace, p.name AS pod
ORDER BY namespace, pod;


// ===========================================================================
// 4. Identity & RBAC — who can do what
// ===========================================================================

// Service account -> the cluster roles it is bound to.
MATCH (b:KubernetesClusterRoleBinding)-[:SUBJECT]->(sa:KubernetesServiceAccount),
      (b)-[:ROLE_REF]->(cr:KubernetesClusterRole)
RETURN sa.namespace AS namespace, sa.name AS service_account, cr.name AS cluster_role
ORDER BY namespace, cluster_role;

// Same, but for namespace-scoped RoleBindings.
MATCH (b:KubernetesRoleBinding)-[:SUBJECT]->(sa:KubernetesServiceAccount),
      (b)-[:ROLE_REF]->(r:KubernetesRole)
RETURN b.namespace AS namespace, sa.name AS service_account, r.name AS role;

// Pods and the service account they run as.
MATCH (p:KubernetesPod)-[:USES_SERVICE_ACCOUNT]->(sa:KubernetesServiceAccount)
RETURN sa.namespace AS namespace, sa.name AS service_account,
       collect(p.name) AS pods
ORDER BY namespace, service_account;

// Service accounts bound to a privileged-sounding role.
MATCH (b:KubernetesClusterRoleBinding)-[:SUBJECT]->(sa:KubernetesServiceAccount),
      (b)-[:ROLE_REF]->(cr:KubernetesClusterRole)
WHERE toLower(cr.name) CONTAINS 'admin' OR toLower(cr.name) CONTAINS 'cluster-admin'
RETURN sa.namespace AS namespace, sa.name AS service_account, cr.name AS cluster_role;

// Every subject (user/group/SA) of every cluster role binding.
MATCH (b:KubernetesClusterRoleBinding)-[:SUBJECT]->(subject)
RETURN b.name AS binding, labels(subject)[0] AS subject_type, subject.name AS subject
ORDER BY binding;


// ===========================================================================
// 5. Storage
// ===========================================================================

// PVC -> PV -> StorageClass.
MATCH (pvc:KubernetesPersistentVolumeClaim)-[:BOUND_TO]->(pv:KubernetesPersistentVolume),
      (pvc)-[:USES_STORAGE_CLASS]->(sc:KubernetesStorageClass)
RETURN pvc.namespace AS namespace, pvc.name AS pvc, pv.name AS pv, sc.name AS storage_class;

// Pods that reference a PVC.
MATCH (p:KubernetesPod)-[:REFERENCES]->(pvc:KubernetesPersistentVolumeClaim)
RETURN p.namespace AS namespace, p.name AS pod, pvc.name AS pvc;


// ===========================================================================
// 6. Containers & images
// ===========================================================================

// Containers and their images.
MATCH (c:KubernetesContainer)
RETURN c.namespace AS namespace, c.name AS container, c.image AS image
ORDER BY namespace, container;

// Containers running privileged-ish settings worth reviewing.
MATCH (c:KubernetesContainer)
WHERE c.allow_privilege_escalation = true OR c.run_as_non_root = false
RETURN c.namespace AS namespace, c.name AS container,
       c.allow_privilege_escalation AS allow_priv_esc, c.run_as_non_root AS run_as_non_root;

// Distinct images in use.
MATCH (c:KubernetesContainer) RETURN DISTINCT c.image AS image ORDER BY image;


// ===========================================================================
// 7. Ad-hoc exploration
// ===========================================================================

// Everything attached to one namespace (change the name).
MATCH (ns:KubernetesNamespace {name: 'cartography'})-[:CONTAINS]->(x)
RETURN labels(x)[0] AS kind, x.name AS name
ORDER BY kind, name;

// One pod and all of its direct neighbours (change the pod name).
MATCH (p:KubernetesPod {name: 'cartography-neo4j-65757f8c6c-ljcgm'})-[r]-(x)
RETURN type(r) AS relationship, labels(x)[0] AS neighbour, x.name AS name;

// Shortest path between a pod and a service account (change names).
MATCH (p:KubernetesPod {name: 'argocd-server-749fd5c977-qc4pl'}),
      (sa:KubernetesServiceAccount {name: 'argocd-server'})
MATCH path = shortestPath((p)-[*..6]-(sa))
RETURN path;

// Secrets metadata (type only — content is never stored).
MATCH (s:KubernetesSecret)
RETURN s.namespace AS namespace, s.name AS name, s.type AS type
ORDER BY namespace, name;
