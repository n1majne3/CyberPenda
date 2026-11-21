const directive =
  /^\s*(eslint-disable|eslint-enable|eslint |global |globals |exported |@ts-|<reference |prettier-ignore|@vite-ignore|#__PURE__|@__PURE__)/

export default {
  meta: {
    type: 'suggestion',
    docs: {
      description: 'Ban comments. Record decisions in docs/adr/ instead.',
    },
    messages: {
      noComment:
        'Do not add comments. Record decisions in docs/adr/. See .claude/skills/delete-comment/SKILL.md.',
    },
    schema: [],
  },
  create(context) {
    return {
      Program() {
        for (const comment of context.sourceCode.getAllComments()) {
          const text = comment.type === 'Line' && comment.value.startsWith('/') ? comment.value.slice(1) : comment.value
          if (directive.test(text)) continue
          context.report({ loc: comment.loc, messageId: 'noComment' })
        }
      },
    }
  },
}
