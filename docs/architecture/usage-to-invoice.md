# Od spotřeby k faktuře: ticks, ocenění, kredit a databázový model

Billing postupně odpovídá na několik různých otázek: co zákazník spotřeboval, kolik tato spotřeba stojí, kolik pokryje kredit a co se objeví na konkrétní faktuře. `RatedUsageGroup` propojuje naměřenou spotřebu s účetním výsledkem. Uchovává společný cenový význam mnoha měření a jejich přesnou finanční hodnotu, takže malé částky nezmizí při zaokrouhlování.

Tento dokument spojuje vysvětlení z konverzace „Výpočet v ticks“ s konkrétním databázovým modelem projektu. Vede jeden malý příklad celým tokem a pak ukazuje změny cen, čerpání kreditu, zaokrouhlování, pozdní spotřebu a faktury ze zadání.

![Usage events to invoice](../diagrams/usage-to-invoice/usage-to-invoice.png)

[Editovatelný Mermaid zdroj](../diagrams/usage-to-invoice/usage-to-invoice.mmd).

Schéma níže vychází z [inbox migrace 001](../../migrations/001_usage_inbox.sql) a [účetní migrace 002](../../migrations/002_billing_model.sql), připravené v [PR #8](https://github.com/pupitooo/e2b-billing-api/pull/8). [Migrace 003](../../migrations/003_assignment_seed.sql) obsahuje počáteční katalog. Migrace 004 převádí kredit na přesné ticks. Sdílené Go výpočty ratingu, kreditu, zaokrouhlení a UTC měsíců jsou implementované v `internal/accounting`; worker je zatím nezapisuje do DB a fakturační tabulky jsou návrh. Aktuální kontrakt popisují [finanční pravidla](accounting-rules.md).

Graf zachovává požadované pořadí `Credits → Rounding`. Uživatel 9. října 2026 potvrdil čerpání kreditu v ticks před zaokrouhlením. [Migrace 004](../../migrations/004_exact_credit.sql) proto převádí zůstatky, alokace a ledger na přesné ticks. Dřívější centový návrh je níže zachovaný jako historická alternativa, nikoli jako aktuální politika.

## 1. Proč nestačí ukládat všechno v centech

Představme si cenu ze zadání: **0,05 USD za 1_000_000 jednotek** `cpu_seconds`. V tomto příkladu jedna CPU sekunda znamená jednu jednotku. Cena za milion je tedy 5 centů.

Platforma neposílá jedno velké měření. Pošle tisíc samostatných událostí, každou po 1_000 jednotkách:

```text
1_000 events × 1_000 units = 1_000_000 units
```

Cena jednoho měření je:

```text
1_000_000 units -> 5 cents
    1_000 units -> 0.005 cent -> USD 0.000_05
```

Pokud každé měření hned zaokrouhlíme na celé centy, dostaneme nulu. Tisíckrát nula je stále nula, přestože milion jednotek má stát 5 centů. Výsledek by závisel na tom, zda platforma pošle jedno měření, nebo tisíc menších.

Proto používáme menší přesnou jednotku peněz:

```text
1 cent = 1_000_000 ticks
1 USD  = 100_000_000 ticks
1 tick = USD 0.000_000_01
```

Jedna malá událost nyní neztratí svou cenu:

```text
1_000 units -> 0.005 cent -> 5_000 ticks
```

Skupina sečte přesné hodnoty:

```text
event #1          5_000 ticks
event #2          5_000 ticks
...
event #1_000       5_000 ticks
----------------------------
total         5_000_000 ticks
              = 5 cents
              = USD 0.05
```

Je to podobné jako sčítat malé délky v milimetrech a až výsledek zobrazit v metrech. Kdybychom každý malý úsek nejprve zaokrouhlili na celé metry, část skutečné délky by zmizela.

**Nezaokrouhluj jednotlivá měření. Uchovej jejich přesnou hodnotu a zaokrouhluj kumulativní součet příslušné skupiny.** Ticks samy neurčují způsob zaokrouhlení; pouze zabrání předčasné ztrátě přesnosti.

## 2. Jeden příklad celým tokem

Zákazník Cyberdyne spotřebuje před 15. říjnem celkem milion jednotek. Platforma je doručí jako uvedených tisíc malých událostí. V tomto prvním průchodu zákazník nemá kredit ani doplněk.

| Úroveň | Co v příkladu znamená | Kde se hodnota nachází |
| --- | --- | --- |
| Usage events | Tisíc měření po 1_000 jednotkách, s identitou, sandboxem a intervalem. | Tisíc řádků `usage_inbox`, každý s `units = 1_000`. |
| Rating | Každému měření přiřadit historickou cenu 5 centů za milion; přesná cena jednoho je 5_000 ticks. | Cena v `price_versions`; čistý Go výpočet `accounting.Rate`; budoucí writer uloží vazby v `usage_ratings`. |
| RatedUsageGroup | Jedna skupina s milionem jednotek a přesnou hrubou cenou 5 milionů ticks. | `rated_usage_groups.total_units = 1_000_000`, `exact_charge_ticks = 5_000_000`. |
| Credits | Žádný dostupný kredit, žádný debet. Hrubá a splatná spotřeba jsou stejné. | Zůstatek v `customer_billing_state`; alokace skupiny je nula. |
| Rounding | Za celou skupinu získáme 5 centů. | Výpočet; současné schéma má centovou projekci `booked_charge_cents`. |
| InvoiceLine | Neměnná položka „CPU usage for October 2026“, částka 5 centů. | Navrhovaná tabulka `invoice_lines`, zatím neexistuje. |
| Invoice | Říjnový doklad Cyberdyne, součet položek 5 centů. | Navrhovaná tabulka `invoices`, zatím neexistuje. |

Výpočet faktury tak nemusí znovu načíst a ocenit každou malou událost. Pracuje s několika již oceněnými skupinami. Jednotlivé události přitom zůstávají dohledatelné přes vazby, takže agregace neznamená ztrátu původu účtované částky.

## 3. Usage events: co se skutečně spotřebovalo

Jedna událost říká například: „Sandbox zákazníka Cyberdyne spotřeboval v tomto intervalu 1_000 `cpu_seconds`.“ Je to přírůstek k započtení právě jednou, nikoli opakovaně zasílaný celoživotní stav čítače.

V DB je to jeden řádek `usage_inbox`:

| Sloupce | Význam |
| --- | --- |
| `source`, `event_id` | Složený primární klíč měření; stejná identita se používá i při retry nebo změně dávkování. |
| `schema_version` | Kladná verze kontraktu události, dodaná explicitně. |
| `customer_id`, `sandbox_id`, `metric` | Kdo spotřeboval jaký prostředek a ve kterém sandboxu. |
| `period_start`, `period_end` | Interval spotřeby v `timestamptz`; konec musí být pozdější než začátek. Pro rating pracujeme s intervalem `[start, end)`. |
| `units` | Nezáporný celočíselný `bigint`; ještě neobsahuje peníze. |
| `received_at` | Čas přijetí dodaný serverem. Neurčuje historickou cenu spotřeby. |
| `processed_at`, `processing_error` | Stav účetního zpracování. Úspěšné dokončení nemůže současně mít nevyřešenou chybu. |

Inbox nemá finanční sloupce ani `group_id`. Nemá ani cizí klíče na zákazníka a metriku: může trvale zachovat vstup, pro který zatím chybí katalog, a následné zpracování musí problém viditelně evidovat.

Unikátní identita brání dvěma stejným řádkům. Ingestion navíc porovnává obsah: totožný retry zachová původní hodnoty a čas přijetí, jiné údaje pod stejným ID jsou konflikt. Úspěšný příjem do inboxu potvrzuje trvalé uložení; není potvrzením, že byl spotřebován kredit nebo vystavena faktura.

Význam jednotky je součást kontraktu. Pokud budeme chtít zlomky CPU sekund, musíme například definovat jinou celočíselnou jednotku měření; ticks jsou jemnější jednotka **peněz**, ne automatické řešení zlomků resource units.

## 4. Rating: kolik spotřeba podle tehdejší ceny stojí

Rating provádí čistý Go výpočet `accounting.Rate`, který budoucí worker připojí k transakčnímu zápisu. V tomto schématu není samostatná tabulka `rating`, která by sama prováděla ocenění. Worker vybere cenu platnou v okamžiku spotřeby a spočítá přesnou hrubou částku.

`price_versions` uchovává `price_version_id`, volitelné `customer_id`, `metric`, `price_per_million_cents` a `effective_from`. `customer_id IS NULL` znamená výchozí cenu. Poslední platná zákaznická cena má přednost před poslední platnou výchozí cenou. Verze jsou append-only; změna ceny nepřepisuje historii.

Pro současný cenový kontrakt platí:

```text
exact_charge_ticks = units × price_per_million_cents
```

Dělení milionem se v tomto násobení neobjeví, protože jeden cent už představuje milion ticks. Pro cenu 5 centů za milion je výsledek `1_000 × 5 = 5_000 ticks`.

Sloupec s ticks používá doménu `billing_ticks` nad přesným PostgreSQL `numeric`. Přijímá pouze nezáporná konečná celá čísla. Agregované `total_units` také používají celočíselné `numeric`; celkový součet tedy nemusí být omezen rozsahem jednoho eventu typu `bigint`. Go musí použít kontrolovanou celočíselnou aritmetiku nebo libovolně velká celá čísla. Přetečení nesmí potichu změnit cenu. `float64` se pro tyto přesné peněžní výpočty nepoužije.

Cena může mít v tomto modelu pouze celé centy **za milion jednotek**. To stále umožňuje ceny jednotlivých měření hluboko pod centem. Ještě jemnější ceník by potřeboval nové vymezení přesnosti; nelze pouze vložit desetinnou hodnotu do `price_per_million_cents` typu `bigint`.

### Příklad: proč nestačí sečíst všechny units a použít aktuální cenu

Použijeme skutečné změny cen ze zadání: 5 a 6 centů za milion. Dřívější konverzace používala i ilustrativní cenu 8 centů; ta není součástí našeho seed katalogu.

| Spotřeba Cyberdyne | Historická cena | Přesný výsledek |
| --- | --- | --- |
| 1_000_000 jednotek 10. října | 5 centů za milion | 5_000_000 ticks = 5 centů |
| 1_000_000 jednotek 20. října | 6 centů za milion, platí od 15. října | 6_000_000 ticks = 6 centů |
| Celkem | Dvě různé cenové verze | 11_000_000 ticks = 11 centů |

Ocenit celé dva miliony pozdější cenou 6 centů by vyrobilo 12 centů. Ocenit je starou cenou 5 centů by vyrobilo 10 centů. Samotný měsíční součet units neuchovává informaci, kterou sazbou se má která část ocenit.

Acme má po celé ukázkové období vlastní cenu 4 centy za milion. Změna výchozí ceny na 6 centů tuto platnou zákaznickou cenu nepřebije. Pozdní říjnová událost se také neoceňuje cenou platnou v listopadovém okamžiku přijetí.

Úspěšné přiřazení je v `usage_ratings(source, event_id, group_id)`. Primární klíč dovolí jedné inbox identitě přispět do jedné skupiny právě jednou. Cizí klíče vyžadují událost i skupinu. Vybraná cena je dohledatelná přes `rated_usage_groups.price_version_id`; rating link neukládá vlastní částku každého eventu.

Každý rating segment musí spadat do jednoho UTC měsíce a jedné platné ceny. Trigger vazby kontroluje zákazníka, metriku a hranice měsíce; nevybírá cenu ani nekontroluje její změnu uprostřed intervalu. Starter rater má nepodporovaný crossing segment odmítnout jako viditelnou chybu zpracování. Rozdělení jednoho receipt mezi více cenových skupin by potřebovalo rozšířit současnou vazbu jedna událost–jedna skupina a určit, jak přesně rozdělit units.

## 5. RatedUsageGroup: oceněný účetní mezivýsledek

`RatedUsageGroup` odpovídá na otázku: **„Kolik stojí tato kompatibilní spotřeba podle konkrétní historické ceny?“** Obsahuje spotřebu i peněžní výsledek. Skupina dovoluje mnoha měřením z různých sandboxů sdílet jednu hranici zaokrouhlení.

V SQL se tabulka jmenuje `rated_usage_groups`. Její unikátní klíč je:

```text
(customer_id, price_version_id, usage_month, billing_month)
```

`metric` je také uložená a musí odpovídat cenové verzi. Všechny částky jsou v USD, proto zde není měnový rozměr. Jiný zákazník, cena, původní měsíc nebo fakturační měsíc znamená jinou skupinu. Sandbox součástí klíče není.

| Sloupec | Co přesně uchovává |
| --- | --- |
| `group_id` | Stabilní identitu skupiny pro rating links a kreditové debety. |
| `customer_id`, `price_version_id`, `metric` | Vlastníka a cenový význam skupiny. |
| `usage_month` | První den původního měsíce spotřeby, odvozený v UTC. |
| `billing_month` | První den období, jehož faktura má částku zahrnout; nesmí být před původním měsícem. |
| `total_units` | Přesný součet započtených jednotek. |
| `exact_charge_ticks` | Přesnou **hrubou cenu před kreditem**. |
| `booked_charge_cents` | Kumulativní zaokrouhlená projekce hrubé ceny; neurčuje přesné čerpání kreditu. |
| `allocated_credit_ticks` | Dosud přidělený kredit skupině v přesných ticks; nejvýše `exact_charge_ticks`. |

V konverzaci se pro hrubou cenu skupiny používal konceptuální název `gross_charge_ticks`. **Skutečný sloupec skupiny je `exact_charge_ticks`.** Název `gross_charge_ticks` existuje v jiné tabulce, `monthly_usage`, která sčítá hrubou cenu všech skupin za původní měsíc zákazníka.

Po tisíci malých měřeních může budoucí worker vytvořit tento obsah:

```text
customer_id             cyberdyne
metric                  cpu_seconds
price_version_id        cpu-default-2026-10-01
usage_month             2026-10-01
billing_month           2026-10-01
total_units             1_000_000
exact_charge_ticks      5_000_000
booked_charge_cents     5
allocated_credit_ticks  0
```

`price_version_id` zde odkazuje na seeded verzi. Centová hodnota je zaokrouhlená hrubá projekce; samotná migrace ji nepočítá.

Skupina je současně pravidlo správnosti: pokud bychom do klíče přidali sandbox nebo batch, každý z nich by dostal vlastní zaokrouhlení a stejné celkové units by mohly vytvořit jinou cenu. Cenu nebo měsíce naopak vynechat nemůžeme, protože bychom smíchali odlišný účetní význam.

Identita skupiny se nesmí měnit; její součty jsou zatím proměnlivé. DB ještě nemá zmrazení po fakturaci. Automaticky ani nedokazuje, že `total_units` odpovídá sumě propojených událostí, ticks odpovídají ceně a centy zvolenému zaokrouhlení. Tyto vztahy musí udržovat a ověřovat budoucí finanční writer.

## 6. Credits: hrubá cena, použitý kredit a zůstatek

Hrubá cena popisuje hodnotu spotřeby. Kredit je jiná operace: říká, jaká část této ceny už bude pokryta prostředky zákazníka.

```text
gross usage charge       USD 10.00 = 1_000 cents = 1_000_000_000 ticks
applied credit          -USD  7.00 =  -700 cents =  -700_000_000 ticks
----------------------------------------------------------------------
payable usage            USD  3.00 =   300 cents =   300_000_000 ticks
```

`exact_charge_ticks` zůstane 1_000_000_000. Nenahradíme ho 300_000_000, protože by se ztratila původní cena spotřeby. Stejně tak kredit nezmění `total_units` ani historickou cenu.

V současném schématu odpovídají kreditům tři místa:

| Místo | Otázka, na kterou odpovídá |
| --- | --- |
| `credit_entries` | Které přidělení nebo čerpání změnilo účet a proč? |
| `customer_billing_state.credit_balance_ticks` | Kolik přesného kreditu je nyní dostupné? |
| `rated_usage_groups.allocated_credit_ticks` | Kolik přesného kreditu má tato skupina zachovat pro budoucí fakturu? |

Ledger má `credit_entry_id`, `customer_id`, stabilní `operation_id`, volitelné `group_id`, znaménkovou částku `amount_ticks` a `recorded_at`. Grant je kladný bez skupiny; čerpání je záporné s vazbou na skupinu téhož zákazníka. Nulový debet se nevytváří. Záznamy jsou append-only a `(customer_id, operation_id)` je unikátní, aby opakování jedné operace nepřidělilo nebo nespotřebovalo kredit podruhé.

Zůstatek je rychlá projekce ledgeru. Budoucí writer musí před čerpáním zamknout řádek `customer_billing_state` a společně změnit zůstatek, ledger, alokaci skupiny a `state_version`. DB sama nevypočítává součet ledgeru ani nekontroluje shodu těchto projekcí.

### Proč se limit počítá z hrubé ceny

Ve výše uvedeném příkladu se pro měsíční limit započte 10 USD, i když zákazník zaplatí za spotřebu jen 3 USD. `monthly_usage.gross_charge_ticks` drží cenu podle **původního UTC měsíce spotřeby**, bez odečtení kreditu a bez přičtení doplňků.

Limit z `customer_billing_state.spend_limit_cents` se převede na stejnou přesnost. Pro nastavený limit platí:

```text
reached = gross_charge_ticks >= spend_limit_cents × 1_000_000
```

Limit 8 USD je tedy při hrubé spotřebě 10 USD dosažený, i když splatná spotřeba je jen 3 USD. Nenastavený limit (`NULL`) znamená bez limitu; nula je dosažená už při nulové spotřebě. Billing dál přijme a zaúčtuje všechna naměřená data. Kredit hradí pouze spotřebu; měsíční `concurrency_pack` za 20 USD jím pokrýt nelze.

### Co přesně znamená kredit před zaokrouhlením

Celý cent kreditu lze přesně převést na ticks. Přesná spotřeba ale může z kreditu odebrat méně než cent:

```text
available credit = 1 cent = 1_000_000 ticks
gross usage = 5_000 ticks
applied credit = min(1_000_000, 5_000) = 5_000 ticks
remaining credit = 995_000 ticks = 0.995 cent
net usage = 0 ticks
```

Původní `credit_balance_cents`, `credit_entries.amount_cents` a `allocated_credit_cents` před migrací 004 byly `bigint`. Nemohly uložit ani tento debet, ani zbývajících 0,995 centu. Debet 5_000 ticks není totéž co jeden cent.

Migrace 004 zavádí nezáporné `credit_balance_ticks` a `allocated_credit_ticks` a znaménkové celočíselné `credit_entries.amount_ticks`. Původní centy násobí milionem a zachovává historii; nekompatibilní starou alokaci převyšující přesnou cenu odmítne atomicky. `accounting.AllocateCredit` čerpá přesný kredit z nové spotřeby. Alokace sledují pořadí transakcí pod zákaznickým zámkem; pozdější grant neopravuje dřívější čerpání. Zlomky kreditu zůstávají na účtu v ticks.

Historický návrh kompatibilní s původními sloupci před migrací 004 místo toho čerpá kredit z nových kumulativních **centových přírůstků** skupiny. Ten zachovává přesné ticks spotřeby, ale operačně nejprve vypočítá hrubé centy a pak přidělí centový kredit. Tyto dvě politiky nelze zaměňovat. Aktuální politika je potvrzené čerpání přesných ticks před zaokrouhlením; historická centová alternativa níže slouží k vysvětlení rozdílu.

## 7. Rounding: kdy a nad čím vzniknou centy

Zaokrouhluje se kumulativní částka kompatibilní skupiny. Raw units ani přesné ticks se tím nemění. Vlastní pravidlo je návrhová volba: zadání vyžaduje centové faktury a ukázkové výsledky, ale neurčuje všechny hraniční případy.

Sdílený výpočet `RoundCents` používá pro nezáporné částky `half-up`, tedy polovinu centu zaokrouhlí nahoru:

```text
round_half_up_cents(ticks) = floor((ticks + 500_000) / 1_000_000)
```

| Přesná částka | Výsledek |
| --- | --- |
| 5_000 ticks = 0,005 centu | 0 centů |
| 499_999 ticks = 0,499_999 centu | 0 centů |
| 500_000 ticks = 0,5 centu | 1 cent |
| 1_000_000 ticks = 1 cent | 1 cent |
| 617_283_945 ticks = 617,283_945 centu | 617 centů = 6,17 USD |

Aktuální `InvoiceAmounts` zaokrouhlí kumulativní gross a net skupiny. Kreditový řádek je záporný rozdíl těchto centových hodnot, nikoli nezávisle zaokrouhlený přesný debet. Součet položek tak odpovídá zaokrouhlenému net. Při gross 1 cent a přesném kreditu 0,5 centu se net 0,5 centu zaokrouhlí na 1 cent; zobrazený kredit je 0 centů, zatímco ledger uchovává přesný debet 500_000 ticks. Budoucí snapshot faktury musí zachovat i tyto přesné částky pro audit.

### Historický návrh před migrací 004: průběžně zaúčtovat jen nový centový přírůstek

Tato část zachovává původní centovou alternativu a její tehdejší názvy sloupců. Zadání umožňuje zjistit zůstatek kreditu už před vystavením faktury. Dřívější návrh proto po každém eventu spočítá zaokrouhlený **celkový stav skupiny**, odečte předchozí zaúčtované centy a čerpá kredit jen z rozdílu:

```text
new_booked_cents = round_half_up_cents(new_exact_charge_ticks)
delta_cents = new_booked_cents - old_booked_charge_cents
credit_debit_cents = min(credit_balance_cents, delta_cents)

booked_charge_cents = new_booked_cents
allocated_credit_cents += credit_debit_cents
credit_balance_cents -= credit_debit_cents
credit_entries.amount_cents = -credit_debit_cents  // only when positive
```

Takto roste naše skupina s měřeními po 1_000 jednotkách a cenou 5 centů za milion:

| Počet událostí | `total_units` | `exact_charge_ticks` | Kumulativní `booked_charge_cents` |
| --- | --- | --- | --- |
| 1 | 1_000 | 5_000 | 0 |
| 100 | 100_000 | 500_000 | 1 |
| 200 | 200_000 | 1_000_000 | 1 |
| 1_000 | 1_000_000 | 5_000_000 | 5 |

Přesná hodnota zůstává zachovaná i tehdy, když se průběžná centová projekce změní. Při přechodu od 100. k 200. události už je jeden cent zaúčtovaný; nepřidáme ho znovu. Součet všech centových přírůstků se rovná zaokrouhlenému výsledku celé skupiny.

To zajišťuje stejnou hrubou cenu při různém rozdělení **téže otevřené skupiny**. Neprokazuje to nezávislost kreditových alokací na pořadí různých skupin, pozdních dat nebo nových grantů. Dřívější návrh bere kredit dostupný v okamžiku zpracování a pozdějším grantem zpětně nepřepisuje historické alokace; tato politika potřebuje explicitní worker testy.

Na fakturu se pak zkopírují již zaúčtované gross centy a alokovaný kredit. Uzávěrka kredit podruhé nečerpá a jednotlivá měření znovu nezaokrouhluje. Tento operační návrh tedy neznamená „první převod na centy až při uzávěrce po přesném čerpání kreditu“; graf a současné centové sloupce je potřeba číst s tímto rozdílem.

## 8. InvoiceLine: co je na tomto konkrétním dokladu

`RatedUsageGroup` uchovává oceněný účetní mezivýsledek. `InvoiceLine` zachovává, co se skutečně objevilo na určité vystavené faktuře. Skupina může během otevřeného období růst; vydaná položka se nesmí změnit pozdějším měřením, ceníkem ani adresou.

Tabulka `invoice_lines` zatím neexistuje. [Logický ERD](../diagrams/data-model/data-model.mmd) navrhuje:

| Navrhovaný sloupec | Význam |
| --- | --- |
| `invoice_line_id`, `invoice_id` | Identitu položky a vazbu na její fakturu. |
| `group_id` | Volitelný odkaz pro usage nebo credit položku. |
| `subscription_id` | Volitelný odkaz pro položku doplňku. |
| `description_snapshot` | Neměnný popis, včetně historické sazby nebo původního měsíce, kde je to potřeba. |
| `amount_cents` | Výslednou znaménkovou částku: spotřeba/doplněk kladně, použitý kredit záporně. |

Budoucí uzávěrka musí zachovat i potřebné množství, metriku, cenu, měsíce a kreditové detaily. Částky ani popisy vystaveného dokladu se nesmí zpětně odvozovat z proměnlivé skupiny nebo katalogu. Potřebná data se snapshotují a skupiny se uzavřou ve stejné transakci.

Aktuální politika odvodí usage a kreditové položky pomocí `InvoiceAmounts(exact_charge_ticks, allocated_credit_ticks)`. Kredity lze prezentačně sloučit součtem již vypočtených centových rozdílů, ale jejich původ a přesné ticks musí zůstat dohledatelné. Sloučení nesmí zavést další zaokrouhlení.

Poplatek za doplněk přijde do faktury jinou cestou: `addon_subscriptions.monthly_price_cents` uchovává cenu při nákupu. Nepotřebuje resource rating ani usage kredit a už je v celých centech. Faktura tedy obsahuje i položky, které nezačaly jako usage event.

Auditní cesta spotřeby má být:

```text
invoice line -> frozen rated group -> usage_ratings -> usage_inbox
                                 -> price_versions
                                 -> credit_entries
```

## 9. Invoice: neměnný měsíční doklad

Tabulka `invoices` také zatím neexistuje. Návrh obsahuje `invoice_id`, `customer_id`, `billing_month_utc`, `customer_sequence`, `address_snapshot`, `total_cents` a `issued_at_utc`. Položky se odkazují na tuto fakturu.

Budoucí schéma musí zajistit jednu fakturu pro `(customer_id, billing_month_utc)` a unikátní `(customer_id, customer_sequence)`. Pořadí je zákaznické: `ACME-0001`, `ACME-0002`; čísla Cyberdyne ho neposouvají. Konceptuální `last_invoice_number` z ERD není v současné `customer_billing_state`, takže i číslování čeká na implementaci.

`total_cents` je přesný celočíselný součet znaménkových položek. Adresa vychází z `customers` v okamžiku vystavení a uloží se do snapshotu. Pozdější změna zákazníka už nesmí ovlivnit vydaný doklad.

### Příklad: celé faktury ze zadání

Následující výsledky ověřují testy čistých Go výpočtů; budoucí runtime a fakturační writer je musí reprodukovat také přes veřejná API:

| Položka | ACME-0001, říjen | ACME-0002, listopad | CYBERDYNE-0001, říjen |
| --- | --- | --- | --- |
| Acme usage, 4 centy/milion | 12,00 USD | 4,00 USD | — |
| Pozdní říjnová Acme usage, 4 centy/milion | — | 2,00 USD | — |
| Usage za 5 centů/milion | — | — | 6,17 USD |
| Usage za 6 centů/milion | — | — | 12,00 USD |
| `concurrency_pack` | 20,00 USD | 20,00 USD | — |
| Použitý kredit | −12,00 USD | −6,00 USD | 0,00 USD |
| **Celkem** | **20,00 USD** | **20,00 USD** | **18,17 USD** |

Acme dostane grant 25 USD (`amount_ticks = 2_500_000_000`). Říjnových 300 milionů units při ceně 4 centy za milion má hodnotu `1_200_000_000 ticks`, tedy 1_200 centů. Použití 12 USD kreditu ponechá 13 USD. Faktura má spotřebu `1_200`, kredit `-1_200` a doplněk `2_000`, celkem `2_000` centů.

V listopadovém dokladu je pozdní říjen za 2 USD a listopad za 4 USD. Jejich skupiny mají oddělené původní měsíce; dohromady spotřebují 6 USD kreditu. Zůstatek klesne ze 13 na 7 USD. Doplněk má dalších 20 USD a kredit ho nehradí. Faktura opět vyjde na 20 USD. Vystavení ani opakovaný požadavek nesmí kredit odečíst ještě jednou.

Cyberdyne má v první sazbě `123_456_789 × 5 = 617_283_945 ticks`, po zaokrouhlení 617 centů. Druhá sazba vytvoří jinou skupinu s `200_000_000 × 6 = 1_200_000_000 ticks`, tedy 1_200 centů. Součet faktury je 1_817 centů. Hrubá přesná říjnová spotřeba je `1_817_283_945 ticks`, nad limitem 15 USD (`1_500_000_000 ticks`). Za listopad se limit vyhodnocuje proti listopadovému gross stavu; není potřeba měnit říjnovou fakturu.

## 10. Pozdní spotřeba: dvě různá období jsou záměr

Acme spotřebuje 50 milionů units 30. října, ale platforma je doručí až po vystavení říjnové faktury. Ocenění stále použije říjnovou zákaznickou cenu 4 centy za milion:

```text
50_000_000 × 4 = 200_000_000 ticks = USD 2.00
```

Nová skupina ponese:

```text
usage_month    2026-10-01
billing_month  2026-11-01
```

Listopadových 100 milionů units bude jiná skupina:

```text
usage_month    2026-11-01
billing_month  2026-11-01
exact_charge_ticks  400_000_000
```

Listopadová faktura tak zahrne 2 USD za říjen a 4 USD za listopad, ale gross projekce pro limity přiřadí 2 USD zpět říjnu. Acme bude mít po doúčtování gross říjen 14 USD a gross listopad 4 USD. Vydaná říjnová faktura zůstane stejná.

Právě proto nestačí jediný sloupec „month“ a právě proto oceněná skupina není totéž co fakturační položka. Jedna popisuje původ a cenu spotřeby, druhá konkrétní doklad, na kterém je částka vyúčtována.

Uzávěrka ještě potřebuje implementovat pevnou hranici přijatých událostí zákazníka. Počká na jejich zpracování bez držení zámku, který potřebuje worker; pod zákaznickým zámkem pak atomicky zmrazí skupiny a vytvoří fakturu s položkami. Nevyřešená chyba před touto hranicí musí uzávěrku blokovat. Pozdější vstup se nasměruje do vhodného otevřeného období. Současné schéma ukládá oba měsíce, ale tuto uzávěrku ani routing samo neprovádí.

## 11. Co musí při zpracování zůstat pohromadě

Worker musí v jedné transakci provést přiřazení události, změny skupiny, změnu gross měsíčního stavu, případný kreditový debet a zůstatek, `state_version` a inbox `processed_at`. Jinak by pád mohl zanechat spotřebovaný kredit bez dokončené události nebo dvojí účinek při retry.

Unikátní `usage_ratings` je potřebná pojistka, ale sama nedokazuje kompletní právě-jednou finanční účinek. Zápisy musejí sdílet transakci a zákaznický zámek. U chyby zůstane input dohledatelný; chybějící zákazník, metrika nebo cena se nesmí tiše změnit na bezplatnou spotřebu.

Subcentová politika je potvrzené čerpání ticks před zaokrouhlením. Čisté Go testy ověřují uvedené částky, polovinu centu, rozdělení spotřeby, cenové a UTC hranice, pozdější granty, routing pozdní spotřeby a přetečení. Worker ještě potřebuje transakční integraci a ověření retry, souběhu a pádu procesu. Uzávěrka navíc potřebuje neměnné snapshoty, idempotentní číslování a shodu součtu řádků s fakturou. Jsou to zbývající implementační úkoly; tento dokument ani diagram je nevydávají za hotové chování.
