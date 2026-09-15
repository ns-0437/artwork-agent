MM_PER_INCH = 25.4


def to_inches(value: float, unit: str) -> float:
    """Converts a declared dimension to inches. `unit` is 'in' or 'mm', per
    the orders.declared_unit CHECK constraint - anything else is treated as
    already-inches defensively rather than raising, since this is a purely
    internal call from api-go, not untrusted customer input.
    """
    if unit == "mm":
        return value / MM_PER_INCH
    return value
