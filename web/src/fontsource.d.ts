// Side-effect CSS imports from @fontsource font packages have no TypeScript
// types of their own. Declare them as empty modules so the bare imports in
// main.tsx typecheck under the strict app tsconfig.
declare module "@fontsource-variable/*";
declare module "@fontsource/*";
