package sources

import "os"

// readFile is a seam so certificate loading can be exercised in tests.
var readFile = os.ReadFile
