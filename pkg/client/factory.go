/*
Copyright 2017, 2019 the Velero contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package client

import (
	kbclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	veleroclient "github.com/vmware-tanzu/velero/pkg/client"
	clientset "github.com/vmware-tanzu/velero/pkg/generated/clientset/versioned"
)

type factory struct {
	flags       *pflag.FlagSet
	clientConfig *rest.Config
	kubeconfig  string
	kubecontext string
	baseName    string
	namespace   string
	clientQPS   float32
	clientBurst int
}

// NewFactory returns a Factory.
func NewFactory(baseName string, clientConfig *rest.Config) veleroclient.Factory {
	f := &factory{
		flags:    pflag.NewFlagSet("", pflag.ContinueOnError),
		baseName: baseName,
	}
	f.namespace = velerov1api.DefaultNamespace
	f.clientConfig = clientConfig
	return f
}

func (f *factory) BindFlags(flags *pflag.FlagSet) {
	flags.AddFlagSet(f.flags)
}

func (f *factory) ClientConfig() (*rest.Config, error) {
	return f.clientConfig, nil
}

func (f *factory) Client() (clientset.Interface, error) {
	clientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}

	veleroClient, err := clientset.NewForConfig(clientConfig)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return veleroClient, nil
}

func (f *factory) KubeClient() (kubernetes.Interface, error) {
	clientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}

	kubeClient, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return kubeClient, nil
}

func (f *factory) DynamicClient() (dynamic.Interface, error) {
	clientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return dynamicClient, nil
}

func (f *factory) KubebuilderClient() (kbclient.Client, error) {
	clientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	velerov1api.AddToScheme(scheme)
	kubebuilderClient, err := kbclient.New(clientConfig, kbclient.Options{
		Scheme: scheme,
	})

	if err != nil {
		return nil, err
	}

	return kubebuilderClient, nil
}

func (f *factory) SetBasename(name string) {
	f.baseName = name
}

func (f *factory) SetClientQPS(qps float32) {
	f.clientQPS = qps
}

func (f *factory) SetClientBurst(burst int) {
	f.clientBurst = burst
}

func (f *factory) Namespace() string {
	return f.namespace
}
