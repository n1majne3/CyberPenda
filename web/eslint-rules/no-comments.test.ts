import { RuleTester } from 'eslint'
import tseslint from 'typescript-eslint'
import { describe, it } from 'vitest'
import rule from './no-comments.js'

RuleTester.describe = describe
RuleTester.it = it

new RuleTester({ languageOptions: { parser: tseslint.parser } }).run('no-comments', rule, {
  valid: [
    'const a = "// not a comment"',
    '// eslint-disable-next-line no-console\nconsole.log(1)',
    '/* eslint-disable no-console */',
    '// @ts-expect-error test\nconst b: number = "x"',
    '/// <reference types="vitest/globals" />',
  ],
  invalid: [
    { code: '// explains\nconst a = 1', errors: [{ messageId: 'noComment' }] },
    { code: 'const a = 1 /* block */', errors: [{ messageId: 'noComment' }] },
    { code: '/** doc */\nexport function f() {}', errors: [{ messageId: 'noComment' }] },
    {
      code: 'const v = <div>{/* jsx */}</div>',
      languageOptions: { parserOptions: { ecmaFeatures: { jsx: true } } },
      errors: [{ messageId: 'noComment' }],
    },
  ],
})
