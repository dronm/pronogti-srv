const integers = new Intl.NumberFormat("ru-RU");
const dates = new Intl.DateTimeFormat("ru-RU", {
	dateStyle: "short",
	timeStyle: "short",
});

export const paymentAmount = (kopecks: unknown): string => {
	if (typeof kopecks !== "number" || !Number.isSafeInteger(kopecks)) {
		return "—";
	}
	const amount = BigInt(Math.abs(kopecks));
	const whole = integers.format(amount / 100n);
	const fraction = String(amount % 100n).padStart(2, "0");
	return `${kopecks < 0 ? "−" : ""}${whole},${fraction} ₽`;
};

export const paymentDate = (value: Date | null | undefined): string =>
	value instanceof Date && Number.isFinite(value.getTime()) ? dates.format(value) : "—";

export const paymentMethod = (value: string | null | undefined): string =>
	value === "sbp" ? "СБП" : value === "card" ? "Карта" : value || "—";

export const paymentEnvironment = (value: string | null | undefined): string =>
	value === "production" ? "Рабочая" : value === "sandbox" ? "Тестовая" : value === "mock" ? "Имитация" : value || "—";

const statusLabels: Record<string, string> = {
	prepared: "Подготовлен",
	payment_pending: "Ожидает оплаты",
	paid: "Оплачен",
	cancelled: "Отменён",
	expired: "Истёк",
	creating: "Создаётся",
	pending: "Ожидается",
	failed: "Ошибка",
	unknown: "Требует проверки",
	not_started: "Не начато",
	processing: "В обработке",
	succeeded: "Выполнено",
	demo: "Демо",
	blocked: "Заблокировано",
	issued: "Выдан",
	processed: "Обработано",
	ignored: "Пропущено",
};

export const paymentStatus = (value: string): string => statusLabels[value] ?? value;

export const shortPaymentId = (id: string): string => id.slice(0, 8);

export const paymentOrderRoute = (id: string) => ({
	name: "paymentOrderDetail",
	params: { id },
});
