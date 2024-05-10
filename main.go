package main

import (
	"context"
	"errors"
	"fmt"
	flags "github.com/jessevdk/go-flags"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Opts struct {
	SourceEKSContext         string `long:"sourceEKSContext" description:"Name of source EKS [Elastic Kubernetes Systems] context" required:"true"`
	TargetEKSContext         string `long:"targetEKSContext" description:"Name of target EKS [Elastic Kubernetes Systems] context" required:"true"`
	SourceEFSDNSName         string `long:"sourceEFSDNSName" description:"Name of EFS [Elastic Filesystem] DNS of source EKS" required:"true"`
	TargetEFSDNSName         string `long:"targetEFSDNSName" description:"Name of EFS [Elastic Filesystem] DNS of target EKS" required:"true"`
	SourceStorageClass       string `long:"sourceStorageClass" description:"Name of source Storage Class in Kubernetes" default:"efs"`
	TargetStorageClass       string `long:"targetStorageClass" description:"Name of target Storage Class in Kubernetes" default:"efs"`
	MountArgs                string `long:"mountArgs" description:"Arguments to mount EFS"  default:"-t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport"`
	RsyncArgs                string `long:"rsyncArgs" description:"Arguments to rysnc EFS"  default:"-rulpEto"`
	PvcIncludeNamespaceRegex string `long:"pvcIncludeNamespaceRegex" description:"Regular expression to select namespace of PVCs to synchronize."  default:"default"`
	PvcIncludeNameRegex      string `long:"pvcIncludeNameRegex" description:"Regular expression to select names of PVCs to synchronize."  default:".*"`
	DryRun                   bool   `long:"dryRun" description:"Dry-Run of configuration"`
	Quiet                    bool   `long:"quiet" description:"Turn off verbose output"`
}

var (
	opts Opts
)

func main() {
	parse(&opts)

	// get-info
	log("start")
	sourceClient := getK8sClientForContext(opts.SourceEKSContext)
	log("SourceEKSContext loaded successfully")

	targetClient := getK8sClientForContext(opts.TargetEKSContext)
	log("TargetEKSContext loaded successfully")

	storageClassParamsSource := getStorageClassParameters(sourceClient, opts.SourceStorageClass)
	fileSystemIdSource := storageClassParamsSource["fileSystemId"]
	log(fmt.Sprintf("StorageClassSource fileSystemId: %s", fileSystemIdSource))

	storageClassParamsTarget := getStorageClassParameters(targetClient, opts.TargetStorageClass)
	fileSystemIdTarget := storageClassParamsTarget["fileSystemId"]
	log(fmt.Sprintf("StorageClassTarget fileSystemId: %s", fileSystemIdTarget))

	pvcsSource := getPVCs(sourceClient, opts.SourceStorageClass, opts.PvcIncludeNamespaceRegex, opts.PvcIncludeNameRegex)
	log(fmt.Sprintf("There are %d pvcs in the source cluster that match selection", len(pvcsSource)))

	pvcsTarget := getPVCs(targetClient, opts.TargetStorageClass, opts.PvcIncludeNamespaceRegex, opts.PvcIncludeNameRegex)
	log(fmt.Sprintf("There are %d pvcs in the target cluster that match selection", len(pvcsTarget)))

	// mount
	mountSource := mountEFS("source-", fileSystemIdSource, opts.SourceEFSDNSName, opts.MountArgs)
	mountTarget := mountEFS("target-", fileSystemIdTarget, opts.TargetEFSDNSName, opts.MountArgs)

	// createMissingPVCs
	for attempt := 1; attempt <= 10; attempt++ {
		log(fmt.Sprintf("creating missing PVCs on target, attempt %d...", attempt))
		created := createMissingPVCs(targetClient, opts.TargetStorageClass, pvcsSource, pvcsTarget)
		log(fmt.Sprintf("%d pvcs created", len(created)))
		if len(created) == 0 {
			break
		}
		log("Waiting pvs to be created...")
		time.Sleep(60)
		pvcsTarget = getPVCs(targetClient, opts.TargetStorageClass, opts.PvcIncludeNamespaceRegex, opts.PvcIncludeNameRegex)
	}

	// rsync
	rsyncDirs(pvcsSource, pvcsTarget, mountSource, mountTarget, opts.RsyncArgs)
	log("end")
}

func parse(opts *Opts) []string {
	args, err := flags.Parse(opts)
	if flags.WroteHelp(err) {
		os.Exit(0)
	} else {
		fail("parse error", err)
	}
	if len(args) != 0 {
		fail("", errors.New(fmt.Sprintf("Too many arguments: %s", args)))
	}
	return args
}

func exit(err error) {
	if err != nil {
		os.Exit(0)
	}
}

func log(message string) {
	if !opts.Quiet {
		if opts.DryRun {
			message = " [DRY RUN] " + message
		}
		currentTime := time.Now()
		fmt.Println(currentTime.Format("2006-01-02T15:04:05.00Z07:00") + " - INFO - " + message)
	}
}

func fail(message string, err error) {
	if err != nil {
		if message != "" {
			currentTime := time.Now()
			fmt.Println(currentTime.Format("2006-01-02T15:04:05.00Z07:00") + " - ERROR - " + message)
		}
		panic(err)
	}
}

func getK8sClientForContext(context string) *kubernetes.Clientset {
	var kubeconfig string = filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()
	fail(fmt.Sprintf("Fail to build the k8s config for context %s", context), err)

	clientSet, err := kubernetes.NewForConfig(config)
	fail(fmt.Sprintf("Fail to create clientSet for context %s", context), err)

	return clientSet
}

func getStorageClassParameters(clientset *kubernetes.Clientset, storageClassName string) map[string]string {
	ret, err := clientset.StorageV1().StorageClasses().Get(context.TODO(), storageClassName, metav1.GetOptions{})
	fail(fmt.Sprintf("Couldn't get storage class named %s", storageClassName), err)
	return ret.Parameters
}

func getPVCs(clientset *kubernetes.Clientset, storageClassName string, pvcIncludeNamespaceRegex, pvcIncludeNameRegex string) map[string]v1.PersistentVolumeClaim {

	reNamespace := regexp.MustCompile(pvcIncludeNamespaceRegex)
	reName := regexp.MustCompile(pvcIncludeNameRegex)

	pvcs := make(map[string]v1.PersistentVolumeClaim, 0)
	result, err := clientset.CoreV1().PersistentVolumeClaims("").List(context.TODO(), metav1.ListOptions{})
	fail("Couldn't list pvcs", err)

	for _, value := range result.Items {
		if reNamespace.MatchString(value.ObjectMeta.Namespace) && reName.MatchString(value.ObjectMeta.Name) {
			if annotation, _ := value.ObjectMeta.Annotations["volume.beta.kubernetes.io/storage-class"]; *value.Spec.StorageClassName == storageClassName || annotation == storageClassName {
				pvcs[value.ObjectMeta.Namespace+"/"+value.ObjectMeta.Name] = value
			}
		}
	}
	return pvcs
}

func createMissingPVCs(targetClientset *kubernetes.Clientset, targetStorageclass string, sourcePVCs, targetPVCs map[string]v1.PersistentVolumeClaim) []string {
	createdPVCs := make([]string, 0)
	for sourceIndex, sourcePVC := range sourcePVCs {
		if _, ok := targetPVCs[sourceIndex]; !ok {
			newName := createVPC(targetClientset, targetStorageclass, sourceIndex, sourcePVC)
			createdPVCs = append(createdPVCs, newName)
			log("created pvc " + newName)
		}
	}

	if opts.DryRun {
		return []string{}
	} else {
		return createdPVCs
	}
}

func createVPC(clientSet *kubernetes.Clientset, newStorageClass string, name string, pvc v1.PersistentVolumeClaim) (newName string) {
	log("creating pvc " + name)
	createOptions := metav1.CreateOptions{}
	if opts.DryRun {
		createOptions.DryRun = []string{"All"}
	}
	pvcNew := pvc.DeepCopy()

	// update some metadata entries
	pvcNew.SetCreationTimestamp(metav1.Now())
	pvcNew.SetUID("")
	delete(pvcNew.ObjectMeta.Annotations, "pv.kubernetes.io/bind-completed")
	delete(pvcNew.ObjectMeta.Annotations, "pv.kubernetes.io/bound-by-controller")
	pvcNew.Spec.VolumeName = ""
	pvcNew.ObjectMeta.ResourceVersion = ""
	if newStorageClass != "" {
		if *pvcNew.Spec.StorageClassName != "" {
			*pvcNew.Spec.StorageClassName = newStorageClass
		}
		if _, ok := pvcNew.ObjectMeta.Annotations["volume.beta.kubernetes.io/storage-class"]; ok {
			pvcNew.ObjectMeta.Annotations["volume.beta.kubernetes.io/storage-class"] = newStorageClass
		}
	}

	ret, err := clientSet.CoreV1().PersistentVolumeClaims(pvc.ObjectMeta.Namespace).Create(context.TODO(), pvcNew, createOptions)
	fail(fmt.Sprintf("Couldn't create pvc on target %d", name), err)

	return ret.ObjectMeta.Namespace + "/" + ret.ObjectMeta.Name
}

func mountEFS(prefix, fileSystemId string, EFSDNSName, mountArgs string) (mountPath string) {
	mountPath = fmt.Sprintf("/tmp/%s%s", prefix, fileSystemId)
	EFSDNSName = EFSDNSName + ":/"

	log("creating dir...")
	mkdirComand := exec.Command("mkdir", "-p", mountPath)
	fmt.Println(mkdirComand)
	if !opts.DryRun {
		err := mkdirComand.Run()
		fail("Couldn't create dir "+mountPath, err)
	}

	log("mounting NFS...")
	args := strings.Split(mountArgs, " ")
	args = append(args, EFSDNSName)
	args = append(args, mountPath)
	mountComand := exec.Command("mount", args...)
	fmt.Println(mountComand)
	if !opts.DryRun {
		err := mountComand.Run()
		fail("Couldn't mount "+EFSDNSName, err)
	}
	return mountPath
}

func rsyncDirs(pvcsSource, pvcsTarget map[string]v1.PersistentVolumeClaim, mountSource, mountTarget, rsyncArgs string) {
	log("rsyncing dirs...")
	for sourceIndex, sourcePVC := range pvcsSource {
		targetPVC, ok := pvcsTarget[sourceIndex]
		if !ok {
			fail("Couldn't find corresponding pvc on target: "+sourceIndex, errors.New("PVC not found in target"))
		}
		volumeSource := sourcePVC.Spec.VolumeName
		volumeTarget := targetPVC.Spec.VolumeName
		if volumeSource == "" || volumeTarget == "" {
			log("skipping pvc, volume not yet ready: " + sourceIndex)
			continue
		}
		dirSource := filepath.Join(mountSource, volumeSource) + string(os.PathSeparator)
		dirTarget := filepath.Join(mountTarget, volumeTarget) + string(os.PathSeparator)
		rsyncDir(dirSource, dirTarget, rsyncArgs)
	}
}

func rsyncDir(dirSource, dirTarget, rsyncArgs string) {
	args := strings.Split(rsyncArgs, " ")
	args = append(args, dirSource)
	args = append(args, dirTarget)
	execComand := exec.Command("rsync", args...)
	fmt.Println(execComand)
	if !opts.DryRun {
		err := execComand.Run()
		fail("Couldn't rsync "+dirSource, err)
	}
}
