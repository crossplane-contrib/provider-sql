module github.com/crossplane-contrib/provider-sql

go 1.13

require (
	github.com/DATA-DOG/go-sqlmock v1.5.0
	github.com/crossplane/crossplane-runtime v0.12.0
	github.com/crossplane/crossplane-tools v0.0.0-20201007233256-88b291e145bb
	github.com/go-sql-driver/mysql v1.5.0
	github.com/google/go-cmp v0.5.0
	github.com/lib/pq v1.8.0
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.6.1 // indirect
	golang.org/x/crypto v0.0.0-20201002170205-7f63de1d35b0 // indirect
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/yaml.v3 v3.0.0-20200605160147-a5ece683394c // indirect
	k8s.io/api v0.18.8
	k8s.io/apimachinery v0.18.8
	k8s.io/utils v0.0.0-20200603063816-c1c6865ac451
	sigs.k8s.io/controller-runtime v0.6.2
	sigs.k8s.io/controller-tools v0.3.0
)
