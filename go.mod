module github.com/danielxiao/kmotion

go 1.15

require (
	github.com/danielxiao/mig-controller v0.0.0-20210623095844-39e8995ed623
	github.com/konveyor/controller v0.4.1
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/vmware-tanzu/velero v1.6.0
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	sigs.k8s.io/controller-runtime v0.7.1-0.20201215171748-096b2e07c091
)

replace github.com/vmware-tanzu/velero => github.com/konveyor/velero v0.10.2-0.20210517170947-84365048b688
