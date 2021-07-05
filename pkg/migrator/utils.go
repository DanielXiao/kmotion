package migrator

import (
	"context"
	"fmt"
	liberr "github.com/konveyor/controller/pkg/error"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path"
)

func (t *Task) createManifestFile() error {
	backupFilePath := path.Join(t.CacheDir, fmt.Sprintf("%s.tar", t.UID()))
	if _, err := os.Stat(backupFilePath); err == nil {
		t.Logger.Infof("File %s exists, remove it", backupFilePath)
		if err = os.Remove(backupFilePath); err != nil {
			return err
		}
	}
	t.Logger.Infof("Creat %s for backup k8s manifests", backupFilePath)
	backupFile, err := os.Create(backupFilePath)
	if err != nil {
		return err
	}
	t.BackupFile = backupFile
	return nil
}

func (t *Task) cleanManifestFile() error {
	t.Logger.Infof("Remove backup file %s", t.BackupFile.Name())
	if err := t.BackupFile.Close(); err != nil {
		return err
	}
	if err := os.Remove(t.BackupFile.Name()); err != nil {
		return err
	}
	return nil
}

func (t *Task) createDestNamespaces() error {
	destNamespaces := t.destinationNamespaces()
	namespaceList := &corev1.NamespaceList{}
	destClient, err := t.getDestinationClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	err = destClient.List(context.TODO(), namespaceList)
	if err != nil {
		return liberr.Wrap(err)
	}
	for _, ns := range destNamespaces {
		found := false
		for _, nsResource := range namespaceList.Items {
			if ns == nsResource.Name {
				found = true
				break
			}
		}
		if !found {
			nsResource := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
			t.Logger.Infof("Create namespace %s in destination cluster", ns)
			err = destClient.Create(context.TODO(), nsResource)
			if err != nil {
				return liberr.Wrap(err)
			}
		}
	}
	return nil
}

