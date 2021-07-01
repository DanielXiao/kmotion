package migrator

import (
	"context"
	"fmt"
	liberr "github.com/konveyor/controller/pkg/error"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path"
)

func createManifestFile(logger *logrus.Logger, cachePath, name string) (*os.File, error) {
	backupFilePath := path.Join(cachePath, fmt.Sprintf("%s.tar", name))
	if _, err := os.Stat(backupFilePath); err == nil {
		os.Remove(backupFilePath)
	}
	backupFile, err := os.Create(backupFilePath)
	if err != nil {
		return nil, err
	}
	logger.Infof("Created %s for backup k8s manifests", backupFile.Name())
	return backupFile, nil
}

func cleanManifestFile(logger *logrus.Logger, backupFile *os.File) error {
	if err:= backupFile.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupFile.Name()); err != nil {
		return err
	}
	logger.Infof("Removed manifests %s", backupFile.Name())
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

