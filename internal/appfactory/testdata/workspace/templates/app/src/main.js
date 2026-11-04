import { initColorScheme } from '@timlee/design';
import { mount } from 'svelte';
import App from './App.svelte';
import './font.js';
import './theme.override.css';
import './app.css';

initColorScheme();

mount(App, { target: document.getElementById('app') });
