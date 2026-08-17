package syncro

// FromCustomerForTest exposes the side-detection rule to the package's tests
// without making it part of the public surface.
func FromCustomerForTest(cm Comment) bool { return fromCustomer(cm) }
