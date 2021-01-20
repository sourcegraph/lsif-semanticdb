# SemanticDB to LSIF converter ![](https://img.shields.io/badge/status-development-yellow)

Visit https://lsif.dev/ to learn about LSIF.

## Installation

```
go get github.com/sourcegraph/lsif-semanticdb/cmd/lsif-semanticdb
```

## Indexing your repository

To create LSIF index files for your repository, you first [generate SemanticDB files](https://scalameta.org/docs/semanticdb/guide.html)
from the source of your project. This will create a `META-INF/semanticdb` directory somewhere in the project file tree. Convert these
files to LSIF by providing the converter with the path to the generated directory.

```
$ lsif-semanticdb --semanticdbDir core/target/scala-2.12/classes/META-INF/semanticdb \
                  --semanticdbDir lib/target/scala-2.12/classes/META-INF/semanticdb # Optionally pass in multiple --semanticdbDir
.....................................................

97 file(s), 3565 def(s), 89839 element(s)
Processed in 489.327697ms
```

It is expected that the `lsif-semanticdb` command be run from the repository root. Invoking it from a different directory may result in a
mismatch of document URIs that will make paths unresolvable in the Sourcegraph instance at query time.

Use `lsif-semanticdb --help` for more information.
