# eks-volume-synchronizer

Script to synchronize EKS clusters volumes

## Motivation

If you are migrating an EKS cluster in a out-of-place way you'll realize that volumes are more difficult to "migrate". We would like a solution that could "copy/synchronize" volumes from one cluster to the other in a seamless way.


## This solution

This solution uses a golang script to handle this via `persistentVolumeClaims`. We compare then on both source and target cluster, create them if they are missing, and synchronize the volumes using `rsync`. It only works for `EFS` at the moment.
We expect each `pv` to be a directory on a `EFS`.

## Pre-requirements

### Kubernetes Contextes

You'll need two kubeconfig contexts configured on the server.

For EKS clusters they can be configured using this `aws` cli command:

```bash
aws eks update-kubeconfig --name <cluster1> --profile <xxxx>
aws eks update-kubeconfig --name <cluster2> --profile <yyyy>
```

Example:
```bash
[root@host]# aws eks update-kubeconfig --name cluster-blue --profile cluster
Added new context arn:aws:eks:<region>:00000000000:cluster/cluster-blue to /root/.kube/config
[root@host]# aws eks update-kubeconfig --name cluster-green --profile cluster
Added new context arn:aws:eks:<region>:00000000000:cluster/cluster-green to /root/.kube/config
```

### Access to EFS

You'll need physical access to mount NFS volumes to EFS since we are using `rsync` command for the synchronization.

## Usage

You can run the program with `--dryRun` to verify changes.
 - No changes on Kubernetes: missing PVCs on target will be created with dryRun flag as well (to test that they are syntactically valid at least)
 - No changes on operating system: no volumes mounted, no directories created, no rsync (but you'll be able to see the command that would be executed)

Example:
```bash
./eks-volume-synchronizer \
--sourceEKSContext arn:aws:eks:<region>:00000000000:cluster/cluster-blue \
--targetEKSContext arn:aws:eks:<region>:00000000000:cluster/cluster-cluster \
--sourceEFSDNSName fs-xxxxxxxx.efs.<region>.amazonaws.com \
--targetEFSDNSName fs-yyyyyyyy.efs.<region>.amazonaws.com \
--mountArgs='-t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport' \
--rsyncArgs='-rulpEto' \
--pvcIncludeNamespaceRegex=default \
--pvcIncludeNameRegex='claim-.*' \
--dryRun
```

Output:
```yaml
2024-05-10T10:30:40.50-04:00 - INFO -  [DRY RUN] start
2024-05-10T10:30:40.50-04:00 - INFO -  [DRY RUN] SourceEKSContext loaded successfully
2024-05-10T10:30:40.51-04:00 - INFO -  [DRY RUN] TargetEKSContext loaded successfully
2024-05-10T10:30:41.30-04:00 - INFO -  [DRY RUN] StorageClassSource fileSystemId: fs-xxxxxxxx
2024-05-10T10:30:41.84-04:00 - INFO -  [DRY RUN] StorageClassTarget fileSystemId: fs-yyyyyyyy
2024-05-10T10:30:41.87-04:00 - INFO -  [DRY RUN] There are 50 pvcs in the source cluster that match selection
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] There are 0 pvcs in the target cluster that match selection
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] creating dir...
/bin/mkdir -p /tmp/source-fs-xxxxxxxx
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] mounting NFS...
/sbin/mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport fs-xxxxxxxx.efs.<region>.amazonaws.com:/ /tmp/source-fs-xxxxxxxx
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] creating dir...
/bin/mkdir -p /tmp/target-fs-yyyyyyyy
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] mounting NFS...
/sbin/mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport fs-yyyyyyyy.efs.<region>.amazonaws.com:/ /tmp/target-fs-yyyyyyyy
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] creating missing PVCs on target, attempt 1...
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] 0 pvcs created
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] rsyncing dirs...
/usr/bin/rsync -rulpEto /tmp/source-fs-xxxxxxxx/pvc-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa/ /tmp/target-fs-yyyyyyyy/pvc-bbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb/
...
2024-05-10T10:30:41.94-04:00 - INFO -  [DRY RUN] end
```

Once you are satisfied with the output you can remove --dryRun flag to create the missing PVCs and do the synchronization.

## Kubernetes permissions

Read/write persistent volume claims and read permissions on storage classes:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: eks-volume-synchronizer
rules:
- apiGroups: [""]
  resources: ["persistentvolumeclaims"]
  verbs: ["get", "watch", "list", "create"]
- apiGroups: ["storage.k8s.io"]
  resources: ["storageclasses"]
  verbs: ["get", "watch", "list"]
```

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: eks-volume-synchronizer
subjects:
- kind: Group
  name: eks-volume-synchronizer
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: eks-volume-synchronizer
  apiGroup: rbac.authorization.k8s.io
```
