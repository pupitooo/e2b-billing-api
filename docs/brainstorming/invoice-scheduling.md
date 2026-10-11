# Invoice scheduling proposals

## Invoice generation and delivery (proposal)

The following scheduling choices are proposals, not implemented customer settings
or a selected product policy. Both preserve the fixed calendar-month contract.
Final closure and invoice publication happen in the same transaction; delivering
the resulting document is a separate operation. An unfinished preview, if added
later, must remain distinguishable from an issued immutable invoice.

Assume earlier customer months are closed, February is open, and accepted input
can be processed successfully. For the January 2027 invoice:

| Policy | Final closure and invoice generation | Delivery | January usage received on 5 February |
| --- | --- | --- | --- |
| Generate on the first day; deliver on the selected day | 1 February | 20 February | Appears on a later invoice because January is already closed. |
| Generate and deliver on the selected day | 20 February | 20 February | Remains eligible for January if accounting commits before closing acquires the account lock. |

The selected generation day provides a longer window for late input, at the cost
of keeping the preceding month open and its final amount unavailable for longer.
A selected delivery day changes notification timing; it never recalculates an
already issued invoice. Neither choice guarantees that no input arrives later.
Usage accounting and credit allocation continue independently of this schedule.

**Recommended first extension:** run a monthly job on the first UTC day and call
the existing invoice API for the preceding month of each customer. Keep generation
and delivery separate, and add customer-specific generation days only if waiting
for late input or a preferred issue date is a product requirement. For a customer
with generation day 20, run January's close on 20 February and February's close
on 20 March. A missed run must recover earlier missing months in increasing order.
One customer's failed close must not prevent other customers from progressing;
retry the same customer/month without reserving another invoice number.

Before implementing customer schedules, choose the execution time in UTC, the
rule for days 29–31 in shorter months, the effective date of preference changes,
and recovery after outages. Generation and delivery need separate durable work
tracking and retry identities. Delivery retries must not generate another invoice;
provider idempotence, when available, is separate from database invoice uniqueness.

The scheduler, customer day settings, document delivery, and an optional preview
are not implemented. Whole UTC calendar months remain a fixed system contract;
the scheduling alternatives remain open proposals.

Any scheduler must follow the implemented
[completed-month requirement](../architecture/accounting-rules.md#completed-month-requirement).
