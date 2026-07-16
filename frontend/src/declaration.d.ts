declare module '*.svg' {
  const url: string;
  export default url;
}

declare module '*.less' {
  const classes: { [className: string]: string };
  export default classes;
}

declare module '*/settings.json' {
  const value: {
    theme: 'light' | 'dark';
    themeColor: string;
    menuWidth: number;
  };

  export default value;
}

declare module '*.png' {
  const value: string;
  export default value;
}
