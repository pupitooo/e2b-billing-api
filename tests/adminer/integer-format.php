<?php

namespace Adminer {
    // The display hook needs only Plugin's identity; the browser check uses real Adminer.
    abstract class Plugin {}
}

namespace {
    /** Verify exact grouping, type boundaries, and links with literal expected HTML. */
    function testSelectVal(): void
    {
        $plugin = require __DIR__ . '/../../adminer/plugins-enabled/integer-format.php';
        $cases = [
            ['scenario' => 'zero', 'type' => 'integer', 'value' => '0', 'link' => null, 'wantHTML' => '<span data-e2b-integer="0">0</span>'],
            ['scenario' => 'below grouping boundary', 'type' => 'smallint', 'value' => '999', 'link' => null, 'wantHTML' => '<span data-e2b-integer="999">999</span>'],
            ['scenario' => 'first grouped integer', 'type' => 'smallint', 'value' => '1000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1000">1_000</span>'],
            ['scenario' => 'negative below grouping boundary', 'type' => 'int2', 'value' => '-999', 'link' => null, 'wantHTML' => '<span data-e2b-integer="-999">-999</span>'],
            ['scenario' => 'first grouped negative', 'type' => 'int2', 'value' => '-1000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="-1000">-1_000</span>'],
            ['scenario' => 'spend limit', 'type' => 'int4', 'value' => '1500', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1500">1_500</span>'],
            ['scenario' => 'credit balance', 'type' => 'bigint', 'value' => '1300000000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1300000000">1_300_000_000</span>'],
            ['scenario' => 'maximum bigint', 'type' => 'int8', 'value' => '9223372036854775807', 'link' => null, 'wantHTML' => '<span data-e2b-integer="9223372036854775807">9_223_372_036_854_775_807</span>'],
            ['scenario' => 'minimum bigint', 'type' => 'bigint', 'value' => '-9223372036854775808', 'link' => null, 'wantHTML' => '<span data-e2b-integer="-9223372036854775808">-9_223_372_036_854_775_808</span>'],
            ['scenario' => 'null integer uses default renderer', 'type' => 'bigint', 'value' => null, 'link' => null, 'wantHTML' => null],
            ['scenario' => 'numeric whole number from SQL', 'type' => 'numeric', 'value' => '1000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1000">1_000</span>'],
            ['scenario' => 'credit domain', 'type' => 'billing_ticks', 'value' => '1300000000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1300000000">1_300_000_000</span>'],
            ['scenario' => 'signed tick domain', 'type' => 'billing_signed_ticks', 'value' => '-1000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="-1000">-1_000</span>'],
            ['scenario' => 'numeric beyond bigint', 'type' => 'numeric', 'value' => '123456789012345678901234567890', 'link' => null, 'wantHTML' => '<span data-e2b-integer="123456789012345678901234567890">123_456_789_012_345_678_901_234_567_890</span>'],
            ['scenario' => 'decimal alias whole number', 'type' => 'decimal', 'value' => '1000', 'link' => null, 'wantHTML' => '<span data-e2b-integer="1000">1_000</span>'],
            ['scenario' => 'numeric decimal zero scale preserves notation', 'type' => 'numeric', 'value' => '1000.00', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'numeric decimal uses default renderer', 'type' => 'numeric', 'value' => '1000.25', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'float uses default renderer', 'type' => 'float8', 'value' => '1000', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'numeric text uses default renderer', 'type' => 'text', 'value' => '1000', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'integer array uses default renderer', 'type' => '_int8', 'value' => '{1000,2000}', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'invalid integer is never inserted as HTML', 'type' => 'bigint', 'value' => '<script>1000</script>', 'link' => null, 'wantHTML' => null],
            ['scenario' => 'foreign key link remains escaped', 'type' => 'integer', 'value' => '1000', 'link' => '?select=parent&where=1000', 'wantHTML' => '<a href="?select=parent&amp;where=1000"><span data-e2b-integer="1000">1_000</span></a>'],
        ];

        foreach ($cases as $case) {
            $actual = $plugin->selectVal($case['value'], $case['link'], ['type' => $case['type']], $case['value']);

            if ($actual !== $case['wantHTML']) {
                throw new \RuntimeException(sprintf(
                    'selectVal scenario=%s type=%s value=%s link=%s: got %s, want %s',
                    $case['scenario'], $case['type'], var_export($case['value'], true),
                    var_export($case['link'], true), var_export($actual, true), var_export($case['wantHTML'], true)
                ));
            }
        }

        echo 'PASS selectVal: exact integer grouping, bigint boundaries, default rendering, and links', PHP_EOL;
    }

    testSelectVal();
}
