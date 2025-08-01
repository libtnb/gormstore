#### GORM backend for libtnb session

Use:
```
import "github.com/libtnb/gormstore"
```

#### Documentation

https://pkg.go.dev/github.com/libtnb/gormstore?tab=doc

#### Example

```go
// initialize and setup cleanup
store := gormstore.New(gorm.Open(...))
```

For more details see [gormstore documentation](https://pkg.go.dev/github.com/libtnb/gormstore?tab=doc).

#### Testing

Just sqlite3 tests:

    go test

All databases using docker:

    ./test

If docker is not local (docker-machine etc):

    DOCKER_IP=$(docker-machine ip dev) ./test

#### License

gormstore is licensed under the MIT license. See [LICENSE](LICENSE) for the full license text.