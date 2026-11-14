// Package textfile writes cubit.prom, the one file Cubit ever writes, for
// node_exporter's textfile collector.
//
// The directory is opened as an os.Root and every name inside it is a constant,
// so no input becomes a path. Writes are atomic: a temp file in the same
// directory, fsync, rename over cubit.prom, fsync of the directory. A flock held
// for the life of the Writer keeps a second cubit from writing at once.
package textfile
