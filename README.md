# SemanticDB to LSIF converter

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
$ lsif-semanticdb --semanticdbDir target/scala-2.12/classes/META-INF/semanticdb
.....................................................

97 file(s), 3565 def(s), 89839 element(s)
Processed in 489.327697ms
```

Use `lsif-semanticdb --help` for more information.
