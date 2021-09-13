module github.com/danielxiao/kmotion

go 1.15

require (
	github.com/google/uuid v1.2.0
	github.com/konveyor/controller v0.4.1
	github.com/konveyor/mig-controller v0.0.0-20210702141352-f5bc7f315744
	github.com/openshift/api v0.0.0-20210105115604-44119421ec6b
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/vmware-tanzu/velero v1.6.0
	github.com/vmware/govmomi v0.26.0
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	k8s.io/utils v0.0.0-20201110183641-67b214c5f920
	sigs.k8s.io/controller-runtime v0.7.1-0.20201215171748-096b2e07c091
)

replace (
	bitbucket.org/ww/goautoneg v0.0.0-20120707110453-75cd24fc2f2c => github.com/markusthoemmes/goautoneg v0.0.0-20190713162725-c6008fefa5b1
	github.com/konveyor/mig-controller => github.com/danielxiao/mig-controller v0.0.0-20210915005913-8b029c8e5725
	github.com/vmware-tanzu/velero => github.com/danielxiao/velero v0.10.2-0.20210706070625-e2256ac337d0
)
