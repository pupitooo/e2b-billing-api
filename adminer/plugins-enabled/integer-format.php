<?php

/** Display exact PostgreSQL whole numbers with presentation-only digit grouping. */
final class E2BIntegerFormat extends Adminer\Plugin
{
    // PostgreSQL query results expose the integer-only billing domains as numeric.
    private const EXACT_NUMBER_TYPES = [
        'smallint', 'integer', 'bigint', 'int2', 'int4', 'int8',
        'numeric', 'decimal', 'billing_ticks', 'billing_signed_ticks',
    ];

    /** Format result cells while leaving other types to Adminer's default renderer. */
    public function selectVal($val, $link, array $field, $original)
    {
        if ($val === null || !in_array($field['type'], self::EXACT_NUMBER_TYPES, true)
            || !preg_match('/^-?[0-9]+$/D', (string) $original)) {
            return null;
        }

        // String grouping preserves every digit, including the full bigint range.
        $grouped = preg_replace('/([0-9])(?=(?:[0-9]{3})+$)/D', '$1_', $original);
        $value = '<span data-e2b-integer="' . $original . '">' . $grouped . '</span>';

        return $link
            ? '<a href="' . htmlspecialchars($link, ENT_QUOTES, 'UTF-8') . '">' . $value . '</a>'
            : $value;
    }

    /** Supply raw values to Adminer 6.1.1's Ctrl/Cmd-click editor before it reads the cell. */
    public function head()
    {
        ?>
<script<?php echo Adminer\nonce(); ?>>
document.addEventListener('click', event => {
    const cell = event.target.closest('td');
    const value = cell && cell.firstElementChild;
    if (value && value.matches('[data-e2b-integer]')) {
        // Adminer's selectClick reads firstChild.alt before falling back to textContent.
        // Delegation also covers rows added by pagination and returned after saves.
        value.alt = value.dataset.e2bInteger;
    }
}, true);
</script>
        <?php
    }
}

return new E2BIntegerFormat();
