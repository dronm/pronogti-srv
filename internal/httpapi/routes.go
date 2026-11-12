package httpapi

import "github.com/dronm/webapp"

// BuildRoutes separates operator device setup from public salon discovery and
// shared customer operations. Company/client IDs are resolved from the session.
func BuildRoutes() []webapp.Route {
	var routes []webapp.Route
	public := webapp.NewGroup("", &routes)
	public.GET("/session", webapp.WithName("session.restore"), webapp.WithService("Customer", "SessionInfo"))
	public.GET("/salons", webapp.WithName("salons.list"), webapp.WithService("Customer", "Salons"))
	public.POST("/salons/{slug}/session", webapp.WithName("salon.select"), webapp.WithService("Customer", "SelectSalon"), webapp.WithBinder(selectSalonBinder))

	customer := webapp.NewGroup("/customer", &routes)
	addCustomerRoutes(customer, "Customer", "customer", "/session")

	device := webapp.NewGroup("/kiosk", &routes)
	device.POST("/session", webapp.WithName("kiosk.bind"), webapp.WithService("Kiosk", "BindKiosk"), webapp.WithBinder(bindKioskBinder))
	device.GET("/session", webapp.WithName("kiosk.session"), webapp.WithService("Kiosk", "SessionInfo"))
	// Keep existing kiosk callers working. The Kiosk service requires a valid
	// device and kiosk-mode context before dispatching every alias below.
	addCustomerRoutes(device, "Kiosk", "kiosk.customer", "/customer-session")
	device.POST("/me/init-qr-pay", webapp.WithName("kiosk.customer.initQR.me"), webapp.WithService("Kiosk", "InitQRPayment"), webapp.WithBinder(kioskQRBinder))
	device.POST("/me/init-card-pay", webapp.WithName("kiosk.customer.initCard.me"), webapp.WithService("Kiosk", "InitCardPayment"), webapp.WithBinder(kioskQRBinder))
	device.GET("/me/payments/{id}", webapp.WithName("kiosk.customer.payment.me"), webapp.WithService("Kiosk", "GetPayment"), webapp.WithBinder(paymentGetBinder))
	return routes
}

func addCustomerRoutes(api webapp.Group, serviceName, namePrefix, sessionPath string) {
	api.POST("/checkouts", webapp.WithName(namePrefix+".checkout.prepare"), webapp.WithService(serviceName, "PrepareCheckout"), webapp.WithBinder(checkoutPrepareBinder))
	api.GET("/checkouts/{id}", webapp.WithName(namePrefix+".checkout.get"), webapp.WithService(serviceName, "GetCheckout"), webapp.WithBinder(checkoutGetBinder))
	api.POST(sessionPath, webapp.WithName(namePrefix+".start"), webapp.WithService(serviceName, "StartCustomer"), webapp.WithBinder(startCustomerBinder))
	api.DELETE(sessionPath, webapp.WithName(namePrefix+".end"), webapp.WithService(serviceName, "EndCustomer"), webapp.WithBinder(customerBinder))
	api.POST(sessionPath+"/touch", webapp.WithName(namePrefix+".touch"), webapp.WithService(serviceName, "TouchCustomer"), webapp.WithBinder(customerBinder))
	api.GET("/company", webapp.WithName(namePrefix+".company"), webapp.WithService(serviceName, "Company"))
	api.GET("/staff", webapp.WithName(namePrefix+".staff"), webapp.WithService(serviceName, "Staff"))
	api.GET("/services", webapp.WithName(namePrefix+".services"), webapp.WithService(serviceName, "Services"), webapp.WithBinder(kioskServicesBinder))
	api.GET("/service-cats", webapp.WithName(namePrefix+".serviceCategories"), webapp.WithService(serviceName, "ServiceCategories"), webapp.WithBinder(kioskCategoriesBinder))
	api.GET("/goods", webapp.WithName(namePrefix+".goods"), webapp.WithService(serviceName, "Goods"), webapp.WithBinder(kioskGoodsBinder))
	api.GET("/me/pay-draft", webapp.WithName(namePrefix+".payDraft"), webapp.WithService(serviceName, "PayDraft"), webapp.WithBinder(customerBinder))
	api.POST("/client/register", webapp.WithName(namePrefix+".register"), webapp.WithService(serviceName, "RegisterClient"), webapp.WithBinder(kioskRegisterBinder))
	api.GET("/payments/{id}", webapp.WithName(namePrefix+".payment.get"), webapp.WithService(serviceName, "GetPayment"), webapp.WithBinder(paymentGetBinder))
	api.POST("/payments/{id}/receipt-email", webapp.WithName(namePrefix+".payment.receiptEmail"), webapp.WithService(serviceName, "SendReceiptEmail"), webapp.WithBinder(receiptEmailBinder))
	api.POST("/init-qr-pay", webapp.WithName(namePrefix+".initQR"), webapp.WithService(serviceName, "InitQRPayment"), webapp.WithBinder(kioskQRBinder))
	api.POST("/init-card-pay", webapp.WithName(namePrefix+".initCard"), webapp.WithService(serviceName, "InitCardPayment"), webapp.WithBinder(kioskQRBinder))
}
