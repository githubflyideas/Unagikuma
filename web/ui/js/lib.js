// Preact + htm, re-exported so views import one module.
import { h, render, Fragment, createContext } from '../vendor/preact/preact.module.js';
import { useState, useEffect, useRef, useMemo, useCallback, useContext, useLayoutEffect } from '../vendor/preact/hooks.module.js';
import htm from '../vendor/htm/htm.module.js';

export const html = htm.bind(h);
export { h, render, Fragment, createContext, useState, useEffect, useRef, useMemo, useCallback, useContext, useLayoutEffect };
