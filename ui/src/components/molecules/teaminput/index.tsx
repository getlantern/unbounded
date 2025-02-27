import { useState, ChangeEvent } from 'react';

function TeamInput() {
  const [inputValue, setInputValue] = useState('');

  const handleInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    setInputValue(event.target.value);
    localStorage.setItem("team_code", event.target.value);
  };

  const storedValue = localStorage.getItem("team_code");
  let placeHolder = "Enter team code here"
  if (storedValue) {
    placeHolder = storedValue
  }
  return (
    <div>
      <input
        type="text"
        value={inputValue}
        onChange={handleInputChange}
        placeholder={placeHolder}
      />
      <p>Current Team: {storedValue}</p>
    </div>
  );
}

export default TeamInput;