package migrator

import (
	"fmt"
	"github.com/sirupsen/logrus"
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
