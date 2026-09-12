package order

// Customer is the buyer.
type Customer struct {
	Name  string
	Email string
	Phone string
	// TaxID is the customer's tax registration (GSTIN in India), if provided.
	TaxID string
}

// Address is a postal address with contact details.
type Address struct {
	Name       string
	Line1      string
	Line2      string
	City       string
	State      string
	Country    string
	PostalCode string
	Phone      string
	Email      string
}

// IsEmpty reports whether no address fields are set.
func (a Address) IsEmpty() bool {
	return a == Address{}
}
